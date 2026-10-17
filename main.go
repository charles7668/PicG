package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/config"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
	"git.charleshnas.dscloud.me/charles7668/PicG/internal/gallery"
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
	configPath := os.Getenv("PICG_CONFIG")
	if configPath == "" {
		configPath = "/app/settings.json"
	}
	databasePath := os.Getenv("PICG_DATABASE")
	if databasePath == "" {
		databasePath = "/app/app.sqlite"
	}
	thumbnailDir := os.Getenv("PICG_THUMBNAILS")
	if thumbnailDir == "" {
		thumbnailDir = "/app/thumbnails"
	}
	return routesWithStorage(root, configPath, databasePath, thumbnailDir, true)
}

func routesWithRoot(root string) http.Handler {
	stateDir := root + "-picg-state"
	return routesWithStorage(root, filepath.Join(stateDir, "settings.json"), filepath.Join(stateDir, "app.sqlite"), filepath.Join(stateDir, "thumbnails"), false)
}

func routesWithConfig(root, configPath, cacheDir string) http.Handler {
	return routesWithStorage(root, configPath, filepath.Join(filepath.Dir(cacheDir), "app.sqlite"), cacheDir, false)
}

func routesWithStorage(root, configPath, databasePath, thumbnailDir string, cleanupWhenIdle bool) http.Handler {
	mux := http.NewServeMux()
	browser := gallery.NewBrowser(root)
	settingsStore, err := config.NewStore(configPath)
	if err != nil {
		panic(err)
	}
	db, err := database.Open(databasePath)
	if err != nil {
		panic(err)
	}
	thumbnailStore, err := thumbnail.NewStore(db, thumbnailDir)
	if err != nil {
		panic(err)
	}
	thumbnailQueue := thumbnail.NewQueue(thumbnailStore)
	ffmpeg := video.New(func() string { return settingsStore.Get().EffectiveFFmpegPath() })
	assets, err := fs.Sub(publicFiles, "public")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /assets/", http.StripPrefix("/", http.FileServer(http.FS(assets))))
	browse := func(r *http.Request) gallery.PageData {
		data := browser.Browse(r.URL.Query().Get("path"), r.URL.Query().Get("q"), requestSort(r))
		thumbnailQueue.Focus(data.CurrentPath)
		return data
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
	mux.HandleFunc("GET /media/{path...}", func(w http.ResponseWriter, r *http.Request) {
		file, info, err := browser.Open(r.PathValue("path"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	})
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
		var identity, input string
		var info os.FileInfo
		var err error
		if kind == "cover" {
			identity, input, info, err = browser.ResolveCover(r.URL.Query().Get("path"))
		} else if kind == "image" || kind == "video" {
			identity, input, info, err = browser.ResolveMedia(r.URL.Query().Get("path"))
		} else {
			http.Error(w, "invalid thumbnail kind", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		thumbnailPath, err := thumbnailQueue.GetOrCreate(r.Context(), thumbnail.Request{
			Kind: kind, Identity: identity, SourcePath: input, Info: info,
		}, func(output string) error {
			const maxGoImageBytes = 6 << 20
			videoSource := kind == "video" || isVideoFilename(input)
			if !videoSource && info.Size() <= maxGoImageBytes {
				if imageErr := thumbnail.RenderImage(input, output); imageErr == nil {
					return nil
				}
			}
			return ffmpeg.RenderThumbnail(context.Background(), input, output, videoSource)
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			log.Printf("thumbnail %q: %v", input, err)
			http.Error(w, "unable to create thumbnail", http.StatusServiceUnavailable)
			return
		}
		file, err := os.Open(thumbnailPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		thumbnailInfo, err := file.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeContent(w, r, thumbnailInfo.Name(), thumbnailInfo.ModTime(), file)
	}
	mux.HandleFunc("GET /thumbnail", func(w http.ResponseWriter, r *http.Request) { serveThumbnail(w, r, "") })
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
