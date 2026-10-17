package gallery

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrInvalidPath = errors.New("invalid gallery path")

var imageExtensions = map[string]struct{}{
	".avif": {}, ".gif": {}, ".jpeg": {}, ".jpg": {}, ".png": {}, ".webp": {},
}

var videoExtensions = map[string]struct{}{
	".avi": {}, ".m4v": {}, ".mkv": {}, ".mov": {}, ".mp4": {}, ".mpeg": {}, ".mpg": {}, ".webm": {},
}

type Browser struct {
	root string
}

type Breadcrumb struct {
	Name string
	Href string
}

type Directory struct {
	Name             string
	Path             string
	Href             string
	CoverURL         string
	CoverOriginalURL string
	PhotoCount       int
	FolderCount      int
	Modified         string
}

type Photo struct {
	Name         string
	Path         string
	URL          string
	ThumbnailURL string
	Kind         string
	Modified     string
	Size         string
}

type PageData struct {
	Root          string
	CurrentPath   string
	CurrentName   string
	ParentHref    string
	Query         string
	Breadcrumbs   []Breadcrumb
	Directories   []Directory
	Photos        []Photo
	RootAvailable bool
	Error         string
}

func NewBrowser(root string) *Browser {
	if root == "" {
		root = "/app/pic"
	}
	return &Browser{root: filepath.Clean(root)}
}

func (b *Browser) Root() string {
	return b.root
}

func (b *Browser) Browse(rawPath, query string) PageData {
	rel, err := cleanRelativePath(rawPath)
	data := PageData{Root: b.root, CurrentPath: rel, Query: strings.TrimSpace(query)}
	if err != nil {
		data.Error = "無效的目錄路徑"
		return data
	}
	data.CurrentName = currentName(rel)
	data.ParentHref = galleryHref(parentPath(rel), "")
	data.Breadcrumbs = breadcrumbs(rel)

	info, err := os.Stat(b.root)
	if err != nil || !info.IsDir() {
		data.Error = fmt.Sprintf("找不到圖片目錄 %s", b.root)
		return data
	}
	data.RootAvailable = true

	absolute := filepath.Join(b.root, filepath.FromSlash(rel))
	if !isWithinRoot(b.root, absolute) {
		data.Error = "無效的目錄路徑"
		return data
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			data.Error = "找不到這個目錄"
		} else {
			data.Error = "無法讀取這個目錄"
		}
		return data
	}

	needle := strings.ToLower(data.Query)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		entryPath := joinRelative(rel, entry.Name())
		if entry.IsDir() {
			if needle != "" && !strings.Contains(strings.ToLower(entry.Name()), needle) {
				continue
			}
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			data.Directories = append(data.Directories, Directory{
				Name:             entry.Name(),
				Path:             entryPath,
				Href:             galleryHref(entryPath, ""),
				CoverURL:         coverURL(entryPath),
				CoverOriginalURL: coverOriginalURL(entryPath),
				Modified:         formatDate(entryInfo.ModTime()),
			})
			continue
		}
		if !isMedia(entry.Name()) || (needle != "" && !strings.Contains(strings.ToLower(entry.Name()), needle)) {
			continue
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		kind := mediaKind(entry.Name())
		data.Photos = append(data.Photos, Photo{
			Name:         strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Path:         entryPath,
			URL:          mediaURL(entryPath),
			ThumbnailURL: thumbnailURL(entryPath, kind),
			Kind:         kind,
			Modified:     formatDate(entryInfo.ModTime()),
			Size:         formatBytes(entryInfo.Size()),
		})
	}

	sort.Slice(data.Directories, func(i, j int) bool {
		return strings.ToLower(data.Directories[i].Name) < strings.ToLower(data.Directories[j].Name)
	})
	sort.Slice(data.Photos, func(i, j int) bool {
		return strings.ToLower(data.Photos[i].Name) < strings.ToLower(data.Photos[j].Name)
	})
	return data
}

func (b *Browser) Open(rawPath string) (*os.File, os.FileInfo, error) {
	rel, err := cleanRelativePath(rawPath)
	if err != nil || rel == "" || !isMedia(rel) {
		return nil, nil, ErrInvalidPath
	}
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrInvalidPath
	}
	return file, info, nil
}

func (b *Browser) ResolveVideo(rawPath string) (string, os.FileInfo, error) {
	rel, absolute, info, err := b.ResolveMedia(rawPath)
	if err != nil || !isVideo(rel) {
		return "", nil, ErrInvalidPath
	}
	return absolute, info, nil
}

func (b *Browser) ResolveMedia(rawPath string) (string, string, os.FileInfo, error) {
	rel, err := cleanRelativePath(rawPath)
	if err != nil || rel == "" || !isMedia(rel) {
		return "", "", nil, ErrInvalidPath
	}
	absolute := filepath.Join(b.root, filepath.FromSlash(rel))
	if !isWithinRoot(b.root, absolute) {
		return "", "", nil, ErrInvalidPath
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return "", "", nil, err
		}
		return "", "", nil, ErrInvalidPath
	}
	return rel, resolved, info, nil
}

func (b *Browser) OpenCover(directory string) (*os.File, os.FileInfo, error) {
	rel, err := b.findCover(directory)
	if err != nil {
		return nil, nil, err
	}
	return b.Open(rel)
}

func (b *Browser) ResolveCover(directory string) (string, string, os.FileInfo, error) {
	identity, err := cleanRelativePath(directory)
	if err != nil {
		return "", "", nil, err
	}
	cover, err := b.findCover(identity)
	if err != nil {
		return "", "", nil, err
	}
	_, absolute, info, err := b.ResolveMedia(cover)
	return identity, absolute, info, err
}

func (b *Browser) findCover(directory string) (string, error) {
	rel, err := cleanRelativePath(directory)
	if err != nil || !isWithinRoot(b.root, filepath.Join(b.root, filepath.FromSlash(rel))) {
		return "", ErrInvalidPath
	}
	absolute := filepath.Join(b.root, filepath.FromSlash(rel))
	var cover string
	visited := 0
	_ = filepath.WalkDir(absolute, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if visited >= 500 {
			return fs.SkipAll
		}
		visited++
		if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && isImage(entry.Name()) {
			rel, err := filepath.Rel(b.root, filePath)
			if err == nil {
				cover = filepath.ToSlash(rel)
			}
			return fs.SkipAll
		}
		return nil
	})
	if cover == "" {
		return "", fs.ErrNotExist
	}
	return cover, nil
}

func cleanRelativePath(raw string) (string, error) {
	if strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") {
		return "", ErrInvalidPath
	}
	if raw != "" && strings.Contains("/"+raw+"/", "/../") {
		return "", ErrInvalidPath
	}
	clean := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if clean == "." {
		clean = ""
	}
	return clean, nil
}

func isWithinRoot(root, target string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isImage(name string) bool {
	_, ok := imageExtensions[strings.ToLower(filepath.Ext(name))]
	return ok
}

func isVideo(name string) bool {
	_, ok := videoExtensions[strings.ToLower(filepath.Ext(name))]
	return ok
}

func isMedia(name string) bool {
	return isImage(name) || isVideo(name)
}

func mediaKind(name string) string {
	if isVideo(name) {
		return "video"
	}
	return "image"
}

func currentName(rel string) string {
	if rel == "" {
		return "相片"
	}
	return path.Base(rel)
}

func parentPath(rel string) string {
	if rel == "" || !strings.Contains(rel, "/") {
		return ""
	}
	return path.Dir(rel)
}

func joinRelative(base, name string) string {
	if base == "" {
		return name
	}
	return path.Join(base, name)
}

func breadcrumbs(rel string) []Breadcrumb {
	crumbs := []Breadcrumb{{Name: "相片", Href: galleryHref("", "")}}
	if rel == "" {
		return crumbs
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		crumbs = append(crumbs, Breadcrumb{Name: part, Href: galleryHref(strings.Join(parts[:i+1], "/"), "")})
	}
	return crumbs
}

func galleryHref(rel, query string) string {
	values := url.Values{}
	if rel != "" {
		values.Set("path", rel)
	}
	if query != "" {
		values.Set("q", query)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/?" + encoded
	}
	return "/"
}

func mediaURL(rel string) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/media/" + strings.Join(parts, "/")
}

func coverURL(rel string) string {
	return "/thumbnail?" + url.Values{"kind": []string{"cover"}, "path": []string{rel}}.Encode()
}

func coverOriginalURL(rel string) string {
	return "/cover-original?" + url.Values{"path": []string{rel}}.Encode()
}

func thumbnailURL(rel, kind string) string {
	return "/thumbnail?" + url.Values{"kind": []string{kind}, "path": []string{rel}}.Encode()
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Format("2006-01-02")
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
