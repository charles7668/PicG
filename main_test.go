package main

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRoutesBrowseAndServePictures(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Trips"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestJPEG(t, filepath.Join(root, "root.jpg"))
	writeTestJPEG(t, filepath.Join(root, "Trips", "sunset.jpg"))
	if err := os.WriteFile(filepath.Join(root, "Trips", "clip.mp4"), []byte("fake-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := routesWithRoot(root)
	tests := []struct {
		path        string
		status      int
		want        string
		wantAbsent  string
		contentType string
	}{
		{path: "/", status: http.StatusOK, want: "<!doctype html>", contentType: "text/html"},
		{path: "/", status: http.StatusOK, want: "data-lightbox-prev", contentType: "text/html"},
		{path: "/", status: http.StatusOK, want: "data-lightbox-zoom", contentType: "text/html"},
		{path: "/", status: http.StatusOK, want: "hx-sync", contentType: "text/html"},
		{path: "/", status: http.StatusOK, want: "gallery-sort", contentType: "text/html"},
		{path: "/assets/app.js", status: http.StatusOK, want: "ArrowLeft", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "setLightboxZoom", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "MediaSource", contentType: "text/javascript"},
		{path: "/gallery?path=Trips", status: http.StatusOK, want: "photo-placeholder", wantAbsent: "正在準備縮圖", contentType: "text/html"},
		{path: "/gallery?path=Trips&q=missing", status: http.StatusOK, want: "找不到符合的項目", contentType: "text/html"},
		{path: "/gallery/thumbnails?path=Trips", status: http.StatusOK, want: "sunset", contentType: "text/html"},
		{path: "/media/Trips/sunset.jpg", status: http.StatusOK, contentType: "image/jpeg"},
		{path: "/media/Trips/clip.mp4", status: http.StatusOK, contentType: "video/mp4"},
		{path: "/thumbnail?kind=image&path=Trips%2Fsunset.jpg", status: http.StatusOK, contentType: "image/jpeg"},
		{path: "/cover-original?path=Trips", status: http.StatusOK, contentType: "image/jpeg"},
		{path: "/cover?path=Trips", status: http.StatusOK, contentType: "image/jpeg"},
		{path: "/health", status: http.StatusOK, want: "ok", contentType: "text/plain"},
		{path: "/assets/app.css", status: http.StatusOK, want: ":root", contentType: "text/css"},
		{path: "/assets/app.js", status: http.StatusOK, want: "localStorage", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "picg-video-volume", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "assignVideoSrc", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "videoVolumeAssignment", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "persistVideoVolume", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "cancelThumbnailWork", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "navigatesCurrentPage", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "event.stopPropagation()", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "EventSource", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "/thumbnail-events", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "BroadcastChannel", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "navigator.locks", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "/thumbnail-focus", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "thumbnailFocusInterval", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "maxThumbnailRequests", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "thumbnailCached", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "resumeThumbnails", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "thumbnailRequest", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "X-Video-Direct", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "startFilterPoll", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, want: "compositionstart", contentType: "text/javascript"},
		{path: "/", status: http.StatusOK, want: "dataset.composing", contentType: "text/html"},
		{path: "/assets/app.js", status: http.StatusOK, want: "applyFilterFragment", contentType: "text/javascript"},
		{path: "/assets/app.js", status: http.StatusOK, wantAbsent: "renderFolderCard", contentType: "text/javascript"},
		{path: "/gallery?path=Trips", status: http.StatusOK, want: "data-thumbnail-url", contentType: "text/html"},
		{path: "/missing", status: http.StatusNotFound, want: "page not found", contentType: "text/plain"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			recorder := getPath(t, handler, tt.path)
			if recorder.Code != tt.status {
				t.Fatalf("expected status %d, got %d", tt.status, recorder.Code)
			}
			if !utf8.Valid(recorder.Body.Bytes()) && strings.HasPrefix(tt.contentType, "text/") {
				t.Fatal("text response body is not valid UTF-8")
			}
			body := recorder.Body.String()
			if tt.want != "" && !strings.Contains(body, tt.want) {
				t.Fatalf("response does not contain %q", tt.want)
			}
			if tt.wantAbsent != "" && strings.Contains(body, tt.wantAbsent) {
				t.Fatalf("response unexpectedly contains %q", tt.wantAbsent)
			}
			if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, tt.contentType) {
				t.Fatalf("expected content type containing %q, got %q", tt.contentType, contentType)
			}
		})
	}
}

func TestThumbnailMissReturns503ThenServesCache(t *testing.T) {
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "sunset.jpg"))
	handler := routesWithRoot(root)
	path := "/thumbnail?kind=image&path=sunset.jpg"

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, path, nil))
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected miss status 503, got %d", first.Code)
	}
	if cache := first.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Fatalf("expected no-store on generating response, got %q", cache)
	}
	if retry := first.Header().Get("Retry-After"); retry != "1" {
		t.Fatalf("expected Retry-After 1, got %q", retry)
	}

	ready := waitForThumbnail(t, handler, path)
	if cache := ready.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("expected immutable cache header, got %q", cache)
	}
	if contentType := ready.Header().Get("Content-Type"); !strings.Contains(contentType, "image/jpeg") {
		t.Fatalf("expected jpeg, got %q", contentType)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, path, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("expected cached status 200, got %d", second.Code)
	}
	if cache := second.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("expected immutable cache header, got %q", cache)
	}
}

func TestThumbnailEventsNotifyWhenReady(t *testing.T) {
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "sunset.jpg"))
	server := httptest.NewServer(routesWithRoot(root))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/thumbnail-events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected event-stream, got %q", contentType)
	}

	missResponse, err := http.Get(server.URL + "/thumbnail?kind=image&path=sunset.jpg")
	if err != nil {
		t.Fatal(err)
	}
	missResponse.Body.Close()
	if missResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected miss status 503, got %d", missResponse.StatusCode)
	}

	reader := bufio.NewReader(response.Body)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read events: %v", err)
		}
		if strings.Contains(line, `"status":"ready"`) && strings.Contains(line, "sunset.jpg") {
			return
		}
	}
	t.Fatal("did not receive ready thumbnail event")
}

func TestThumbnailPumpRetriesWhenSrcAlreadySet(t *testing.T) {
	handler := routesWithRoot(t.TempDir())
	recorder := getPath(t, handler, "/assets/app.js")
	js := recorder.Body.String()
	start := strings.Index(js, "const pumpThumbnails = () => {")
	if start < 0 {
		t.Fatal("pumpThumbnails not found")
	}
	pump := js[start:]
	if end := strings.Index(pump, "\n  const releaseThumbnail"); end > 0 {
		pump = pump[:end]
	}
	if !strings.Contains(pump, `image.getAttribute("src")`) || !strings.Contains(pump, `"&retry="`) {
		t.Fatal("pumpThumbnails must change src with retry when the image already has one")
	}
	prepare := js
	if at := strings.Index(js, "const prepareThumbnails"); at >= 0 {
		prepare = js[at:]
	}
	if !strings.Contains(prepare, "thumbnailCached") || !strings.Contains(prepare, "scheduleThumbnail(image)") {
		t.Fatal("prepareThumbnails must only queue listing cache hits")
	}
}

func TestGalleryMarksCachedThumbnails(t *testing.T) {
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "ready.jpg"))
	writeTestJPEG(t, filepath.Join(root, "pending.jpg"))
	handler := routesWithRoot(root)
	waitForThumbnail(t, handler, "/thumbnail?kind=image&path=ready.jpg")

	recorder := getPath(t, handler, "/")
	body := recorder.Body.String()
	if strings.Count(body, `data-thumbnail-cached="true"`) != 1 {
		t.Fatalf("expected one cached thumbnail mark, got %q", body)
	}
	if !strings.Contains(body, "data-thumbnail-url") {
		t.Fatal("expected thumbnail urls on listing")
	}
}

func TestGalleryEnqueueMissingThumbnails(t *testing.T) {
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "photo.jpg"))
	handler := routesWithRoot(root)
	listing := getPath(t, handler, "/")
	if listing.Code != http.StatusOK {
		t.Fatalf("listing status %d", listing.Code)
	}
	if strings.Contains(listing.Body.String(), `data-thumbnail-cached="true"`) {
		t.Fatal("uncached listing should not mark thumbnails cached")
	}
	leave := httptest.NewRequest(http.MethodPost, "/thumbnail-focus", strings.NewReader("tab=reload&leave=1"))
	leave.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(httptest.NewRecorder(), leave)
	waitForThumbnail(t, handler, "/thumbnail?kind=image&path=photo.jpg")
}

func TestThumbnailFocusAcceptsTabView(t *testing.T) {
	handler := routesWithRoot(t.TempDir())
	request := httptest.NewRequest(http.MethodPost, "/thumbnail-focus", strings.NewReader("tab=tab-a&path=album"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", recorder.Code)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/thumbnail-focus", strings.NewReader("path=album")))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("expected missing tab status 400, got %d", missing.Code)
	}
}

func TestThumbnailMissDoesNotWaitForGeneration(t *testing.T) {
	root := t.TempDir()
	const n = 8
	for i := range n {
		writeTestJPEG(t, filepath.Join(root, fmt.Sprintf("p%d.jpg", i)))
	}
	handler := routesWithRoot(root)
	codes := make(chan int, n)
	begin := time.Now()
	for i := range n {
		go func(i int) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/thumbnail?kind=image&path=p%d.jpg", i), nil))
			codes <- recorder.Code
		}(i)
	}
	for range n {
		if code := <-codes; code != http.StatusServiceUnavailable {
			t.Fatalf("expected miss status 503, got %d", code)
		}
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("thumbnail misses blocked the handler: %s", elapsed)
	}
}

func TestVideoSupportsInlineRangeRequests(t *testing.T) {
	root := t.TempDir()
	video := []byte("fake-mp4-video-content")
	if err := os.WriteFile(filepath.Join(root, "clip.mp4"), video, 0o644); err != nil {
		t.Fatal(err)
	}
	handler := routesWithRoot(root)
	request := httptest.NewRequest(http.MethodGet, "/media/clip.mp4", nil)
	request.Header.Set("Range", "bytes=5-11")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("expected status 206, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("expected video/mp4, got %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("expected inline content disposition, got %q", got)
	}
	if got := recorder.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("expected byte ranges, got %q", got)
	}
	if got := recorder.Header().Get("Content-Range"); got != "bytes 5-11/22" {
		t.Fatalf("unexpected content range %q", got)
	}
	if got := recorder.Body.String(); got != string(video[5:12]) {
		t.Fatalf("unexpected range body %q", got)
	}
}

func TestVideoRouteStreamsFFmpegMP4(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed in this test environment")
	}
	root := t.TempDir()
	input := filepath.Join(root, "clip.mkv")
	command := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-c:v", "mpeg4", "-y", input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create video fixture: %v: %s", err, output)
	}
	handler := routesWithRoot(root)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/video/clip.mkv?start=0.5", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("expected video/mp4, got %q", got)
	}
	if got := recorder.Header().Get("X-Video-Duration"); got == "" {
		t.Fatal("expected video duration header")
	}
	if !strings.Contains(recorder.Body.String(), "ftyp") || !strings.Contains(recorder.Body.String(), "moof") {
		t.Fatal("expected fragmented MP4 output")
	}
}

func TestVideoRouteServesDirectMP4(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed in this test environment")
	}
	root := t.TempDir()
	input := filepath.Join(root, "clip.mp4")
	command := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-an", "-y", input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("libx264 is unavailable: %v: %s", err, output)
	}
	handler := routesWithRoot(root)
	head := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodHead, "/video/clip.mp4", nil)
	handler.ServeHTTP(head, request)
	if head.Code != http.StatusOK {
		t.Fatalf("expected HEAD 200, got %d: %s", head.Code, head.Body.String())
	}
	if got := head.Header().Get("X-Video-Direct"); got != "1" {
		t.Fatalf("expected direct play, got %q", got)
	}
	if got := head.Header().Get("X-Video-Duration"); got == "" {
		t.Fatal("expected video duration header")
	}
	if head.Body.Len() != 0 {
		t.Fatal("HEAD should not write a body")
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/video/clip.mp4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Video-Direct"); got != "1" {
		t.Fatalf("expected direct play, got %q", got)
	}
	if got := recorder.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("expected byte ranges, got %q", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "ftyp") {
		t.Fatal("expected mp4 output")
	}
	if strings.Contains(body, "moof") {
		t.Fatal("direct mp4 should not be transcoded to fragmented mp4")
	}
}

func TestGalleryFilterPollsFolderFragment(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "旅行", "日本"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestJPEG(t, filepath.Join(root, "旅行", "日本", "京都.jpg"))
	writeTestJPEG(t, filepath.Join(root, "root.jpg"))
	handler := routesWithRoot(root)

	page := getPath(t, handler, "/gallery?media=image")
	if page.Code != http.StatusOK {
		t.Fatalf("expected gallery 200, got %d", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(body, "data-filter-pending") || !strings.Contains(body, "data-filter-url") {
		t.Fatal("pending filter should expose a fragment poll URL")
	}
	if !strings.Contains(body, "/gallery/filter?") {
		t.Fatal("pending filter should poll /gallery/filter")
	}
	if strings.Contains(body, `hx-trigger="load delay:300ms"`) {
		t.Fatal("pending filter should not reload #gallery-content")
	}
	if !strings.Contains(body, `id="folder-area"`) {
		t.Fatal("pending filter should keep a folder mount point")
	}

	deadline := time.Now().Add(2 * time.Second)
	var raw string
	for time.Now().Before(deadline) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/gallery/filter?media=image", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected filter 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
			t.Fatalf("expected HTML fragment, got %q", contentType)
		}
		raw = recorder.Body.String()
		if strings.Contains(raw, "photo-card") || strings.Contains(raw, "data-photo-grid") || strings.Contains(raw, "id=\"gallery-content\"") {
			t.Fatal("filter fragment should not include photos or the gallery shell")
		}
		if !strings.Contains(raw, `id="folder-area"`) || !strings.Contains(raw, `data-photo-count="1"`) {
			t.Fatalf("expected folder-area with photo count, got %s", raw)
		}
		if !strings.Contains(raw, "data-filter-pending") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(raw, "data-filter-pending") {
		t.Fatal("filter fragment stayed pending")
	}
	if !strings.Contains(raw, "旅行") || !strings.Contains(raw, "data-folder-path") {
		t.Fatalf("expected matching folder card, got %s", raw)
	}
}

func getPath(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	if strings.HasPrefix(path, "/thumbnail") || strings.HasPrefix(path, "/cover?") || strings.HasPrefix(path, "/video-thumbnail") {
		return waitForThumbnail(t, handler, path)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func waitForThumbnail(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var recorder *httptest.ResponseRecorder
	for time.Now().Before(deadline) {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			return recorder
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("thumbnail %s still generating, last status %d", path, recorder.Code)
	return recorder
}

func writeTestJPEG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			picture.Set(x, y, color.RGBA{R: 30, G: 90, B: 180, A: 255})
		}
	}
	if err := jpeg.Encode(file, picture, nil); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsPersistCustomFFmpegPath(t *testing.T) {
	root := t.TempDir()
	handler := routesWithRoot(root)
	form := url.Values{"ffmpeg_path": {"/custom/bin/ffmpeg"}}
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d: %s", recorder.Code, recorder.Body.String())
	}
	page := httptest.NewRecorder()
	routesWithRoot(root).ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/settings?saved=1", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/custom/bin/ffmpeg") || !strings.Contains(page.Body.String(), "設定已儲存") {
		t.Fatalf("unexpected settings page: %d %s", page.Code, page.Body.String())
	}
}

func TestMissingPictureRootShowsSetupMessage(t *testing.T) {
	recorder := httptest.NewRecorder()
	routesWithRoot(filepath.Join(t.TempDir(), "missing")).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "找不到圖片目錄") {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}
