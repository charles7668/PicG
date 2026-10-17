package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		{path: "/missing", status: http.StatusNotFound, want: "page not found", contentType: "text/plain"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			handler.ServeHTTP(recorder, request)
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
	configPath := filepath.Join(t.TempDir(), "settings.json")
	handler := routesWithConfig(root, configPath, filepath.Join(t.TempDir(), "cache"))
	form := url.Values{"ffmpeg_path": {"/custom/bin/ffmpeg"}}
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d: %s", recorder.Code, recorder.Body.String())
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/custom/bin/ffmpeg") {
		t.Fatalf("custom FFmpeg path was not persisted: %s", data)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/settings?saved=1", nil))
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
