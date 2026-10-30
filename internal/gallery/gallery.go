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
	"sync"
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
	root       string
	mediaMu    sync.Mutex
	mediaCache map[mediaCacheKey]mediaCacheEntry
	mediaScan  map[mediaCacheKey]struct{}
}

type mediaCacheKey struct {
	root   string
	query  string
	filter string
}

type mediaCacheEntry struct {
	kinds     map[string]uint8
	createdAt time.Time
	complete  bool
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
	CoverCached      bool
	PhotoCount       int
	FolderCount      int
	Modified         string
	ModifiedAt       time.Time
	SizeBytes        int64
}

type Photo struct {
	Name            string
	Path            string
	URL             string
	ThumbnailURL    string
	ThumbnailCached bool
	Kind            string
	Modified        string
	Size            string
	ModifiedAt      time.Time
	SizeBytes       int64
}

type PageData struct {
	Root          string
	CurrentPath   string
	CurrentName   string
	ParentHref    string
	Query         string
	Sort          string
	MediaFilter   string
	FilterPending bool
	Breadcrumbs   []Breadcrumb
	Directories   []Directory
	Photos        []Photo
	RootAvailable bool
	Error         string
}

type SortOption struct {
	Value string
	Label string
}

var SortOptions = []SortOption{
	{Value: "name", Label: "名稱 A → Z"},
	{Value: "name-desc", Label: "名稱 Z → A"},
	{Value: "date-desc", Label: "日期新到舊"},
	{Value: "date", Label: "日期舊到新"},
	{Value: "size-desc", Label: "檔案大到小"},
	{Value: "size", Label: "檔案小到大"},
}

func NewBrowser(root string) *Browser {
	if root == "" {
		root = "/app/pic"
	}
	return &Browser{root: filepath.Clean(root), mediaCache: make(map[mediaCacheKey]mediaCacheEntry), mediaScan: make(map[mediaCacheKey]struct{})}
}

func (b *Browser) Root() string {
	return b.root
}

func (b *Browser) Browse(rawPath, query, sortKey string) PageData {
	return b.BrowseFiltered(rawPath, query, sortKey, "all")
}

// BrowseFiltered lists the current directory and optionally filters child
// folders by whether their trees contain image or video files.
func (b *Browser) BrowseFiltered(rawPath, query, sortKey, mediaFilter string) PageData {
	rel, err := cleanRelativePath(rawPath)
	data := PageData{Root: b.root, CurrentPath: rel, Query: strings.TrimSpace(query), Sort: normalizeSort(sortKey), MediaFilter: normalizeMediaFilter(mediaFilter)}
	if err != nil {
		data.Error = "無效的目錄路徑"
		return data
	}
	data.CurrentName = currentName(rel)
	data.ParentHref = galleryHref(parentPath(rel), "")
	data.Breadcrumbs = breadcrumbs(rel)

	root, err := os.OpenRoot(b.root)
	if err != nil {
		data.Error = fmt.Sprintf("找不到圖片目錄 %s", b.root)
		return data
	}
	defer root.Close()
	data.RootAvailable = true

	dir, err := root.Open(rootName(rel))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			data.Error = "找不到這個目錄"
		} else {
			data.Error = "無效的目錄路徑"
		}
		return data
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		data.Error = "無效的目錄路徑"
		return data
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		data.Error = "無法讀取這個目錄"
		return data
	}
	absolute := filepath.Join(b.root, filepath.FromSlash(rel))

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
				CoverURL:         coverURL(entryPath, entryInfo.ModTime()),
				CoverOriginalURL: coverOriginalURL(entryPath),
				Modified:         formatDate(entryInfo.ModTime()),
				ModifiedAt:       entryInfo.ModTime(),
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
			URL:          mediaURL(entryPath, kind),
			ThumbnailURL: thumbnailURL(entryPath, kind, entryInfo),
			Kind:         kind,
			Modified:     formatDate(entryInfo.ModTime()),
			Size:         formatBytes(entryInfo.Size()),
			ModifiedAt:   entryInfo.ModTime(),
			SizeBytes:    entryInfo.Size(),
		})
	}
	if data.MediaFilter != "all" && len(data.Directories) > 0 {
		mediaByDirectory, ready := b.directoryMediaKindsCached(absolute, data.Query, data.MediaFilter, data.Directories)
		data.FilterPending = !ready
		filtered := data.Directories[:0]
		for _, directory := range data.Directories {
			kinds := mediaByDirectory[directory.Name]
			if (data.MediaFilter == "image" && kinds&1 != 0) || (data.MediaFilter == "video" && kinds&2 != 0) {
				filtered = append(filtered, directory)
			}
		}
		data.Directories = filtered
	}

	sort.SliceStable(data.Directories, func(i, j int) bool {
		return lessEntry(data.Sort, data.Directories[i].Name, data.Directories[j].Name, data.Directories[i].ModifiedAt, data.Directories[j].ModifiedAt, data.Directories[i].SizeBytes, data.Directories[j].SizeBytes)
	})
	sort.SliceStable(data.Photos, func(i, j int) bool {
		return lessEntry(data.Sort, data.Photos[i].Name, data.Photos[j].Name, data.Photos[i].ModifiedAt, data.Photos[j].ModifiedAt, data.Photos[i].SizeBytes, data.Photos[j].SizeBytes)
	})
	return data
}

// directoryMediaKindsCached schedules a shallow scan of only the visible child
// folders. Results are reused briefly so changing the filter stays responsive.
func (b *Browser) directoryMediaKindsCached(root, query, mediaFilter string, directories []Directory) (map[string]uint8, bool) {
	key := mediaCacheKey{root: root, query: query, filter: mediaFilter}
	b.mediaMu.Lock()
	if cached, ok := b.mediaCache[key]; ok && cached.complete && time.Since(cached.createdAt) < time.Minute {
		b.mediaMu.Unlock()
		return cached.kinds, true
	}
	if _, scanning := b.mediaScan[key]; scanning {
		partial := cloneMediaKinds(b.mediaCache[key].kinds)
		b.mediaMu.Unlock()
		return partial, false
	}
	b.mediaScan[key] = struct{}{}
	visibleDirectories := make([]string, len(directories))
	for i, directory := range directories {
		visibleDirectories[i] = directory.Name
	}
	b.mediaCache[key] = mediaCacheEntry{kinds: make(map[string]uint8), createdAt: time.Now()}
	b.mediaMu.Unlock()

	go func() {
		kinds := directoryMediaKinds(root, visibleDirectories, mediaFilter, func(name string, kind uint8) {
			b.mediaMu.Lock()
			cached := b.mediaCache[key]
			cached.kinds[name] = kind
			b.mediaCache[key] = cached
			b.mediaMu.Unlock()
		})
		b.mediaMu.Lock()
		b.mediaCache[key] = mediaCacheEntry{kinds: kinds, createdAt: time.Now(), complete: true}
		delete(b.mediaScan, key)
		b.mediaMu.Unlock()
	}()
	return nil, false
}

func cloneMediaKinds(kinds map[string]uint8) map[string]uint8 {
	clone := make(map[string]uint8, len(kinds))
	for name, kind := range kinds {
		clone[name] = kind
	}
	return clone
}

func normalizeMediaFilter(value string) string {
	switch value {
	case "image", "video":
		return value
	default:
		return "all"
	}
}

// directoryMediaKinds checks only folders currently shown in the gallery. For
// each one it checks direct files first, then descends as needed, stopping as
// soon as the requested media type is found.
func directoryMediaKinds(root string, directories []string, mediaFilter string, onMatch func(string, uint8)) map[string]uint8 {
	kinds := make(map[string]uint8)
	wantedKind := uint8(1)
	if mediaFilter == "video" {
		wantedKind = 2
	}
	jobs := make(chan string)
	results := make(chan struct {
		name string
		kind uint8
	}, len(directories))
	workerCount := min(8, len(directories))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for name := range jobs {
				kind := uint8(0)
				if directoryContainsMedia(filepath.Join(root, name), wantedKind) {
					kind = wantedKind
				}
				if kind != 0 {
					onMatch(name, kind)
				}
				results <- struct {
					name string
					kind uint8
				}{name: name, kind: kind}
			}
		}()
	}
	go func() {
		for _, name := range directories {
			jobs <- name
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	for result := range results {
		kinds[result.name] = result.kind
	}
	return kinds
}

func directoryContainsMedia(directory string, wanted uint8) bool {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	file, err := os.Open(directory)
	if err != nil {
		return false
	}
	var subdirectories []string
	for {
		entries, readErr := file.ReadDir(64)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				subdirectories = append(subdirectories, entry.Name())
				continue
			}
			matches := (wanted&1 != 0 && isImage(entry.Name())) || (wanted&2 != 0 && isVideo(entry.Name()))
			if matches {
				info, infoErr := entry.Info()
				if infoErr == nil && info.Mode().IsRegular() {
					file.Close()
					return true
				}
			}
		}
		if readErr != nil || len(entries) == 0 {
			break
		}
	}
	file.Close()
	for _, name := range subdirectories {
		if directoryContainsMedia(filepath.Join(directory, name), wanted) {
			return true
		}
	}
	return false
}

func normalizeSort(raw string) string {
	switch raw {
	case "name", "name-desc", "date", "date-desc", "size", "size-desc":
		return raw
	default:
		return "name"
	}
}

func lessEntry(sortKey, nameA, nameB string, timeA, timeB time.Time, sizeA, sizeB int64) bool {
	desc := strings.HasSuffix(sortKey, "-desc")
	switch strings.TrimSuffix(sortKey, "-desc") {
	case "date":
		if !timeA.Equal(timeB) {
			if desc {
				return timeA.After(timeB)
			}
			return timeA.Before(timeB)
		}
	case "size":
		if sizeA != sizeB {
			if desc {
				return sizeA > sizeB
			}
			return sizeA < sizeB
		}
	}
	left, right := strings.ToLower(nameA), strings.ToLower(nameB)
	if desc && strings.TrimSuffix(sortKey, "-desc") == "name" {
		return left > right
	}
	return left < right
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

func (b *Browser) ThumbnailKey(kind, rawPath string) (string, error) {
	rel, err := cleanRelativePath(rawPath)
	if err != nil {
		return "", err
	}
	switch kind {
	case "cover":
		return rel, nil
	case "image", "video":
		if rel == "" || !isMedia(rel) {
			return "", ErrInvalidPath
		}
		return rel, nil
	default:
		return "", ErrInvalidPath
	}
}

func (b *Browser) ResolveMedia(rawPath string) (string, string, os.FileInfo, error) {
	rel, err := cleanRelativePath(rawPath)
	if err != nil || rel == "" || !isMedia(rel) {
		return "", "", nil, ErrInvalidPath
	}
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return "", "", nil, err
	}
	defer root.Close()
	info, err := root.Stat(rootName(rel))
	if err != nil {
		return "", "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", "", nil, ErrInvalidPath
	}
	return rel, filepath.Join(b.root, filepath.FromSlash(rel)), info, nil
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
	if err != nil {
		return "", ErrInvalidPath
	}
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := rel
	if name == "" {
		name = "."
	}
	info, err := root.Stat(rootName(rel))
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrInvalidPath
	}
	entries, err := fs.ReadDir(root.FS(), name)
	if err != nil {
		return "", err
	}
	var directVideo string
	var subdirs []string
	for _, entry := range entries {
		if skipCoverEntry(entry) {
			continue
		}
		if entry.IsDir() {
			subdirs = append(subdirs, entry.Name())
			continue
		}
		if isImage(entry.Name()) {
			return joinRelative(rel, entry.Name()), nil
		}
		if directVideo == "" && isVideo(entry.Name()) {
			directVideo = joinRelative(rel, entry.Name())
		}
	}
	nestedImage, nestedVideo := walkCoverSubdirs(root, rel, subdirs)
	if nestedImage != "" {
		return nestedImage, nil
	}
	if directVideo != "" {
		return directVideo, nil
	}
	if nestedVideo != "" {
		return nestedVideo, nil
	}
	return "", fs.ErrNotExist
}

func walkCoverSubdirs(root *os.Root, rel string, names []string) (string, string) {
	var imageCover, videoCover string
	visited := 0
	fsys := root.FS()
	for _, name := range names {
		if imageCover != "" {
			break
		}
		_ = fs.WalkDir(fsys, joinRelative(rel, name), func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if visited >= 500 {
				return fs.SkipAll
			}
			visited++
			if skipCoverEntry(entry) {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if isImage(entry.Name()) {
				imageCover = filePath
				return fs.SkipAll
			}
			if videoCover == "" && isVideo(entry.Name()) {
				videoCover = filePath
			}
			return nil
		})
	}
	return imageCover, videoCover
}

func skipCoverEntry(entry fs.DirEntry) bool {
	return entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".")
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

func rootName(rel string) string {
	if rel == "" {
		return "."
	}
	return filepath.FromSlash(rel)
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

func mediaURL(rel, kind string) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	prefix := "/media/"
	if kind == "video" {
		prefix = "/video/"
	}
	return prefix + strings.Join(parts, "/")
}

func coverURL(rel string, modified time.Time) string {
	values := url.Values{"kind": []string{"cover"}, "path": []string{rel}}
	if !modified.IsZero() {
		values.Set("v", fmt.Sprintf("%d", modified.UnixNano()))
	}
	return "/thumbnail?" + values.Encode()
}

func coverOriginalURL(rel string) string {
	return "/cover-original?" + url.Values{"path": []string{rel}}.Encode()
}

func thumbnailURL(rel, kind string, info os.FileInfo) string {
	values := url.Values{"kind": []string{kind}, "path": []string{rel}}
	if info != nil {
		values.Set("v", fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size()))
	}
	return "/thumbnail?" + values.Encode()
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
