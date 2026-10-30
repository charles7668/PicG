package main

import (
	"context"
	"embed"
	"errors"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/config"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/gallery"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/migrate"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/thumbnail"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/video"
	"git.charleshnas.dscloud.me/charles7668/PicG/web/components"
	"github.com/a-h/templ"
)

//go:embed public
var publicFiles embed.FS

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("PicG is ready at http://localhost:%s", port)
	log.Fatal(server.ListenAndServe())
}

func routes() http.Handler {
	root := os.Getenv("PIC_DIR")
	if root == "" {
		root = "/app/pic"
	}
	databasePath := os.Getenv("PICG_DATABASE")
	if databasePath == "" {
		databasePath = "/app/app.sqlite"
	}
	thumbnailDir := os.Getenv("PICG_THUMBNAILS")
	if thumbnailDir == "" {
		thumbnailDir = "/app/thumbnails"
	}
	return routesWithStorage(root, databasePath, thumbnailDir, true)
}

func routesWithRoot(root string) http.Handler {
	stateDir := root + "-picg-state"
	return routesWithStorage(root, filepath.Join(stateDir, "app.sqlite"), filepath.Join(stateDir, "thumbnails"), false)
}

func routesWithStorage(root, databasePath, thumbnailDir string, cleanupWhenIdle bool) http.Handler {
	mux := http.NewServeMux()
	browser := gallery.NewBrowser(root)
	db, err := database.OpenWith(databasePath, migrate.Host{ThumbnailDir: thumbnailDir})
	if err != nil {
		panic(err)
	}
	settingsStore, err := config.NewStore(db)
	if err != nil {
		panic(err)
	}
	thumbnailStore, err := thumbnail.NewStore(db, thumbnailDir)
	if err != nil {
		panic(err)
	}
	thumbnailQueue := thumbnail.NewQueue(thumbnailStore)
	thumbnailEvents := thumbnail.NewHub()
	videoInfo := video.NewInfoStore(db)
	ffmpeg := video.New(func() string { return settingsStore.Get().EffectiveFFmpegPath() })
	assets, err := fs.Sub(publicFiles, "public")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /assets/", http.StripPrefix("/", http.FileServer(http.FS(assets))))
	enqueueThumbnail := func(kind, identity, rawPath string) {
		if err := thumbnailQueue.Enqueue(kind, identity, func() error {
			var input string
			var info os.FileInfo
			var resolveErr error
			resolvedIdentity := identity
			if kind == "cover" {
				resolvedIdentity, input, info, resolveErr = browser.ResolveCover(rawPath)
			} else {
				resolvedIdentity, input, info, resolveErr = browser.ResolveMedia(rawPath)
			}
			if resolveErr != nil {
				return resolveErr
			}
			_, err := thumbnailStore.GetOrCreate(context.Background(), thumbnail.Request{
				Kind: kind, Identity: resolvedIdentity, SourcePath: input, Info: info,
			}, func(output string) error {
				const maxGoImageBytes = 6 << 20
				videoSource := kind == "video" || isVideoFilename(input)
				maxDimension, jpegQuality, ffmpegQ := 640, 78, 5
				if kind == "cover" {
					maxDimension, jpegQuality, ffmpegQ = 240, 62, 8
				}
				if !videoSource && info.Size() <= maxGoImageBytes {
					if imageErr := thumbnail.RenderImage(input, output, maxDimension, jpegQuality); imageErr == nil {
						return nil
					}
				}
				return ffmpeg.RenderThumbnail(context.Background(), input, output, videoSource, maxDimension, ffmpegQ)
			})
			status := "ready"
			if err != nil {
				log.Printf("thumbnail %q: %v", input, err)
				status = "error"
			}
			thumbnailEvents.Publish(kind, identity, status)
			return err
		}); err != nil {
			log.Printf("thumbnail enqueue %s %q: %v", kind, identity, err)
		}
	}
	primeListingThumbnails := func(ctx context.Context, data gallery.PageData, includePhotos bool) gallery.PageData {
		items := make([]thumbnail.Presence, 0, len(data.Directories)+len(data.Photos))
		for _, directory := range data.Directories {
			items = append(items, thumbnail.Presence{Kind: "cover", Identity: directory.Path})
		}
		if includePhotos {
			for _, photo := range data.Photos {
				items = append(items, thumbnail.Presence{
					Kind: photo.Kind, Identity: photo.Path, MTimeNS: photo.ModifiedAt.UnixNano(), Size: photo.SizeBytes, MatchStamp: true,
				})
			}
		}
		present, err := thumbnailStore.Present(ctx, items)
		if err != nil {
			log.Printf("thumbnail presence: %v", err)
			present = map[string]bool{}
		}
		for i, directory := range data.Directories {
			if present[thumbnail.PresenceID("cover", directory.Path)] {
				data.Directories[i].CoverCached = true
				continue
			}
			enqueueThumbnail("cover", directory.Path, directory.Path)
		}
		if includePhotos {
			for i, photo := range data.Photos {
				if present[thumbnail.PresenceID(photo.Kind, photo.Path)] {
					data.Photos[i].ThumbnailCached = true
					continue
				}
				enqueueThumbnail(photo.Kind, photo.Path, photo.Path)
			}
		}
		return data
	}
	browse := func(r *http.Request) gallery.PageData {
		return primeListingThumbnails(r.Context(), browser.BrowseFiltered(r.URL.Query().Get("path"), r.URL.Query().Get("q"), requestSort(r), r.URL.Query().Get("media")), true)
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		render(w, r, components.Page(browse(r)))
	})
	mux.HandleFunc("GET /gallery", func(w http.ResponseWriter, r *http.Request) {
		render(w, r, components.GalleryContent(browse(r)))
	})
	mux.HandleFunc("GET /gallery/thumbnails", func(w http.ResponseWriter, r *http.Request) {
		render(w, r, components.ThumbnailGrid(browse(r)))
	})
	mux.HandleFunc("GET /gallery/filter", func(w http.ResponseWriter, r *http.Request) {
		data := primeListingThumbnails(r.Context(), browser.BrowseFiltered(r.URL.Query().Get("path"), r.URL.Query().Get("q"), requestSort(r), r.URL.Query().Get("media")), false)
		w.Header().Set("Cache-Control", "no-store")
		render(w, r, components.FolderArea(data))
	})
	mux.HandleFunc("GET /media/{path...}", func(w http.ResponseWriter, r *http.Request) {
		file, info, err := browser.Open(r.PathValue("path"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("Content-Disposition", "inline")
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	})
	serveVideo := func(w http.ResponseWriter, r *http.Request) {
		rawPath := r.PathValue("path")
		input, stat, err := browser.ResolveVideo(rawPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		identity, keyErr := browser.ThumbnailKey("video", rawPath)
		if keyErr != nil {
			http.NotFound(w, r)
			return
		}
		info, infoErr := videoInfo.Get(r.Context(), identity, input, stat, func(ctx context.Context) (video.Info, error) {
			return ffmpeg.Probe(ctx, input)
		})
		if infoErr != nil {
			log.Printf("video probe %q: %v", input, infoErr)
		}
		start, err := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
		if err != nil || start < 0 || math.IsNaN(start) || math.IsInf(start, 0) {
			start = 0
		}
		if infoErr == nil {
			if start >= info.Duration {
				start = 0
			}
			w.Header().Set("X-Video-Duration", strconv.FormatFloat(info.Duration, 'f', 3, 64))
			if info.Direct {
				w.Header().Set("X-Video-Direct", "1")
			}
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if infoErr == nil && info.Direct {
			file, fileInfo, openErr := browser.Open(rawPath)
			if openErr != nil {
				http.NotFound(w, r)
				return
			}
			defer file.Close()
			w.Header().Set("Cache-Control", "private, max-age=3600")
			w.Header().Set("Content-Disposition", "inline")
			http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("X-Accel-Buffering", "no")
		output := &countingWriter{writer: w}
		if err := ffmpeg.StreamMP4(r.Context(), input, start, output); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			log.Printf("video stream %q: %v", input, err)
			if output.written == 0 {
				w.Header().Del("Content-Disposition")
				w.Header().Del("Content-Type")
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "unable to stream video", http.StatusServiceUnavailable)
			}
		}
	}
	mux.HandleFunc("GET /video/{path...}", serveVideo)
	mux.HandleFunc("HEAD /video/{path...}", serveVideo)
	mux.HandleFunc("GET /cover-original", func(w http.ResponseWriter, r *http.Request) {
		file, info, err := browser.OpenCover(r.URL.Query().Get("path"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	})
	serveThumbnail := func(w http.ResponseWriter, r *http.Request, forcedKind string) {
		kind := forcedKind
		if kind == "" {
			kind = r.URL.Query().Get("kind")
		}
		if kind != "cover" && kind != "image" && kind != "video" {
			http.Error(w, "invalid thumbnail kind", http.StatusBadRequest)
			return
		}
		identity, err := browser.ThumbnailKey(kind, r.URL.Query().Get("path"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		mtimeNS, size, hasStamp := parseThumbVersion(r.URL.Query().Get("v"))
		if cached, ok, cacheErr := thumbnailStore.CachedFile(r.Context(), kind, identity, mtimeNS, size, hasStamp && kind != "cover"); cacheErr == nil && ok {
			serveThumbnailFile(w, r, cached)
			return
		}
		enqueueThumbnail(kind, identity, r.URL.Query().Get("path"))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "1")
		http.Error(w, "thumbnail generating", http.StatusServiceUnavailable)
	}
	mux.HandleFunc("GET /thumbnail", func(w http.ResponseWriter, r *http.Request) { serveThumbnail(w, r, "") })
	mux.HandleFunc("GET /thumbnail-events", thumbnailEvents.Write)
	mux.HandleFunc("POST /thumbnail-focus", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tab := r.Form.Get("tab")
		if tab == "" {
			http.Error(w, "missing tab", http.StatusBadRequest)
			return
		}
		if r.Form.Get("leave") == "1" {
			thumbnailQueue.Leave(tab)
		} else {
			thumbnailQueue.View(tab, r.Form.Get("path"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Compatibility endpoints for bookmarks and pages cached before the unified
	// thumbnail route was introduced.
	mux.HandleFunc("GET /cover", func(w http.ResponseWriter, r *http.Request) { serveThumbnail(w, r, "cover") })
	mux.HandleFunc("GET /video-thumbnail", func(w http.ResponseWriter, r *http.Request) { serveThumbnail(w, r, "video") })
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		settings := settingsStore.Get()
		status := ffmpeg.Status(r.Context())
		render(w, r, components.SettingsPage(components.SettingsData{
			FFmpegPath: settings.FFmpegPath, EffectivePath: settings.EffectiveFFmpegPath(), Available: status.Available,
			Version: status.Version, StatusError: status.Error, Saved: r.URL.Query().Get("saved") == "1",
		}))
	})
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		settings := config.Settings{FFmpegPath: strings.TrimSpace(r.FormValue("ffmpeg_path"))}
		if err := settingsStore.Save(settings); err != nil {
			status := ffmpeg.Status(r.Context())
			renderStatus(w, r, http.StatusInternalServerError, components.SettingsPage(components.SettingsData{
				FFmpegPath: settings.FFmpegPath, EffectivePath: settings.EffectiveFFmpegPath(), Available: status.Available,
				Version: status.Version, StatusError: status.Error, SaveError: "無法儲存設定：" + err.Error(),
			}))
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	handler := securityHeaders(mux)
	if cleanupWhenIdle {
		handler = cleanupAfterIdle(handler, 2*time.Minute, func() {
			removed, err := thumbnailStore.Cleanup(context.Background())
			if err != nil {
				log.Printf("thumbnail cleanup: %v", err)
				return
			}
			if removed > 0 {
				log.Printf("thumbnail cleanup removed %d stale files", removed)
			}
		})
	}
	return handler
}

func cleanupAfterIdle(next http.Handler, idle time.Duration, cleanup func()) http.Handler {
	var mu sync.Mutex
	var timer *time.Timer
	active := 0
	schedule := func() {
		timer = time.AfterFunc(idle, cleanup)
	}
	schedule()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			if active == 0 {
				schedule()
			}
			mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	renderStatus(w, r, http.StatusOK, c)
}

type countingWriter struct {
	writer  io.Writer
	written int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.written += int64(n)
	if flusher, ok := w.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func renderStatus(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

func requestSort(r *http.Request) string {
	if value := strings.TrimSpace(r.URL.Query().Get("sort")); value != "" {
		return value
	}
	if cookie, err := r.Cookie("picg-sort"); err == nil {
		return cookie.Value
	}
	return ""
}

func parseThumbVersion(v string) (mtimeNS, size int64, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
	}
	if mtimeStr, sizeStr, found := strings.Cut(v, "-"); found {
		var err error
		mtimeNS, err = strconv.ParseInt(mtimeStr, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		size, err = strconv.ParseInt(sizeStr, 10, 64)
		if err != nil || size < 0 {
			return 0, 0, false
		}
		return mtimeNS, size, true
	}
	mtimeNS, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return mtimeNS, 0, true
}

func serveThumbnailFile(w http.ResponseWriter, r *http.Request, thumbnailPath string) {
	file, err := os.Open(thumbnailPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func isVideoFilename(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".avi", ".m4v", ".mkv", ".mov", ".mp4", ".mpeg", ".mpg", ".webm":
		return true
	default:
		return false
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
