package service

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"unicode/utf8"
)

// BundleFileEntry is one path in a stored archive (skill or plugin).
type BundleFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// BundleFileContent is one file the admin browser asked to open.
type BundleFileContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

// bundleFileTextLimit is how much of a text file the admin browser is given.
// The archive itself may hold up to the install/decompress cap; dumping that
// into a JSON response would freeze the settings drawer.
const bundleFileTextLimit = 1 << 20 // 1 MiB

// bundleFileImageLimit is the decoded size cap for an inline image preview.
const bundleFileImageLimit = 2 << 20 // 2 MiB

// maxBundleEntryBytes is the hard cap for any single archive member. Anything
// larger is refused outright rather than truncated, because a half-decoded
// image or a truncated binary has no meaning to the UI. This is also the
// defence against a zip bomb stored by a malicious uploader: the install-time
// scanner already rejects oversized bundles, but read time must not trust it.
const maxBundleEntryBytes = 64 << 20 // 64 MiB

const (
	bundleFileEncodingUTF8   = "utf-8"
	bundleFileEncodingBase64 = "base64"
	bundleFileEncodingBinary = "binary"

	// The drawer lists the tree then opens the entry file, and every later
	// click re-reads the same zip. 512 MiB is the install cap, not RAM this
	// cache may pin; a zip over budget is still cached, as the sole occupant.
	bundleArchiveCacheSlots = 8
	bundleArchiveCacheBytes = 64 << 20
)

// errBundleFileMissing is returned by readBundleZipFile when the requested
// member is absent. Both callers map it to a 404.
var errBundleFileMissing = errors.New("bundle file not found")

func listBundleZipFiles(archive []byte) ([]BundleFileEntry, error) {
	if len(archive) == 0 {
		return nil, fmt.Errorf("bundle archive is empty")
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open bundle archive: %w", err)
	}
	out := make([]BundleFileEntry, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		out = append(out, BundleFileEntry{
			Path: f.Name,
			Size: int64(f.UncompressedSize64),
		})
	}
	return out, nil
}

func readBundleZipFile(archive []byte, name string) ([]byte, error) {
	if len(archive) == 0 {
		return nil, fmt.Errorf("bundle archive is empty")
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open bundle archive: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		if f.FileInfo().IsDir() {
			return nil, errBundleFileMissing
		}
		if f.UncompressedSize64 > maxBundleEntryBytes {
			return nil, fmt.Errorf("bundle file %q exceeds size limit", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", name, err)
		}
		defer func() { _ = rc.Close() }()
		body, err := io.ReadAll(io.LimitReader(rc, maxBundleEntryBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", name, err)
		}
		if int64(len(body)) > maxBundleEntryBytes {
			return nil, fmt.Errorf("bundle file %q exceeds size limit", name)
		}
		return body, nil
	}
	return nil, errBundleFileMissing
}

func safeBundleFilePath(relativePath string) (string, error) {
	trimmed := strings.TrimSpace(relativePath)
	if trimmed == "" {
		return "", fmt.Errorf("bundle file path is required")
	}
	if strings.Contains(trimmed, "\\") {
		return "", fmt.Errorf("invalid bundle file path: %s", relativePath)
	}
	if path.IsAbs(trimmed) {
		return "", fmt.Errorf("invalid bundle file path: %s", relativePath)
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("invalid bundle file path: %s", relativePath)
		}
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid bundle file path: %s", relativePath)
	}
	return clean, nil
}

func projectBundleFileContent(rel string, body []byte) *BundleFileContent {
	out := &BundleFileContent{Path: rel, Size: int64(len(body))}
	if mediaType, ok := bundleImageMediaType(rel); ok {
		out.MediaType = mediaType
		if len(body) > bundleFileImageLimit {
			out.Encoding = bundleFileEncodingBinary
			out.Binary = true
			return out
		}
		out.Encoding = bundleFileEncodingBase64
		out.Content = base64.StdEncoding.EncodeToString(body)
		return out
	}
	if bundleFileLooksBinary(body) {
		out.Encoding = bundleFileEncodingBinary
		out.Binary = true
		return out
	}
	out.Encoding = bundleFileEncodingUTF8
	if ext := strings.ToLower(path.Ext(rel)); ext != "" {
		out.MediaType = "text/plain"
		if ext == ".md" || ext == ".markdown" {
			out.MediaType = "text/markdown"
		}
	}
	if len(body) > bundleFileTextLimit {
		out.Content = string(body[:bundleFileTextLimit])
		out.Truncated = true
		return out
	}
	out.Content = string(body)
	return out
}

func bundleFileLooksBinary(body []byte) bool {
	if bytes.IndexByte(body, 0) >= 0 {
		return true
	}
	return !utf8.Valid(body)
}

func bundleImageMediaType(rel string) (string, bool) {
	switch strings.ToLower(path.Ext(rel)) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	case ".bmp":
		return "image/bmp", true
	case ".ico":
		return "image/x-icon", true
	case ".svg":
		return "image/svg+xml", true
	default:
		return "", false
	}
}

type bundleArchiveCache struct {
	mu       sync.Mutex
	entries  []cachedBundleArchive
	slots    int
	maxBytes int
}

type cachedBundleArchive struct {
	key     string
	archive []byte
}

func newBundleArchiveCache() *bundleArchiveCache {
	return &bundleArchiveCache{
		slots:    bundleArchiveCacheSlots,
		maxBytes: bundleArchiveCacheBytes,
	}
}

func (c *bundleArchiveCache) get(key string) []byte {
	if c == nil || key == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, entry := range c.entries {
		if entry.key != key {
			continue
		}
		c.entries = append(c.entries[:i], c.entries[i+1:]...)
		c.entries = append([]cachedBundleArchive{entry}, c.entries...)
		return entry.archive
	}
	return nil
}

func (c *bundleArchiveCache) put(key string, archive []byte) {
	if c == nil || key == "" || len(archive) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, entry := range c.entries {
		if entry.key != key {
			continue
		}
		c.entries = append(c.entries[:i], c.entries[i+1:]...)
		break
	}
	c.entries = append([]cachedBundleArchive{{key: key, archive: archive}}, c.entries...)
	slots, maxBytes := c.slots, c.maxBytes
	if slots <= 0 {
		slots = bundleArchiveCacheSlots
	}
	if maxBytes <= 0 {
		maxBytes = bundleArchiveCacheBytes
	}
	for len(c.entries) > 1 && (len(c.entries) > slots || c.cachedBytes() > maxBytes) {
		c.entries = c.entries[:len(c.entries)-1]
	}
}

func (c *bundleArchiveCache) cachedBytes() int {
	total := 0
	for _, entry := range c.entries {
		total += len(entry.archive)
	}
	return total
}

func bundleCacheKey(kind string, tenantID uint64, digestOrRef string) string {
	return fmt.Sprintf("%s:%d:%s", kind, tenantID, digestOrRef)
}
