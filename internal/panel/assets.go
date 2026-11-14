package panel

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// gzipMinSize is the smallest asset worth compressing. Below it the gzip
// framing overhead makes the response larger than the original.
const gzipMinSize = 1024

// compressibleExt lists the text assets Vite emits. Already compressed formats
// (fonts, images, wasm) are deliberately absent.
var compressibleExt = map[string]bool{
	".js":          true,
	".mjs":         true,
	".css":         true,
	".html":        true,
	".json":        true,
	".map":         true,
	".svg":         true,
	".txt":         true,
	".webmanifest": true,
}

// acceptsGzip reports whether the Accept-Encoding header values allow gzip.
// Multiple header lines are considered, quality factors are honored and "*"
// matches gzip. A malformed factor is treated as 1, as RFC 9110 requires for
// unrecognized values.
func acceptsGzip(values []string) bool {
	var (
		explicit  bool
		explicitQ float64
		wildcard  bool
		wildcardQ float64
	)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			encoding, quality, ok := parseEncoding(part)
			if !ok {
				continue
			}
			switch encoding {
			case "gzip", "x-gzip":
				explicit = true
				if quality > explicitQ {
					explicitQ = quality
				}
			case "*":
				wildcard = true
				if quality > wildcardQ {
					wildcardQ = quality
				}
			}
		}
	}
	// An explicit gzip entry always wins over the wildcard, so
	// "gzip;q=0, *" rejects gzip as RFC 9110 requires.
	if explicit {
		return explicitQ > 0
	}
	return wildcard && wildcardQ > 0
}

// parseEncoding splits one Accept-Encoding element into its token and quality.
func parseEncoding(value string) (encoding string, quality float64, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, false
	}
	quality = 1
	if idx := strings.IndexByte(value, ';'); idx >= 0 {
		params := value[idx+1:]
		value = strings.TrimSpace(value[:idx])
		for _, param := range strings.Split(params, ";") {
			param = strings.TrimSpace(param)
			if !strings.HasPrefix(param, "q=") && !strings.HasPrefix(param, "Q=") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64)
			if err != nil || parsed < 0 || parsed > 1 {
				// RFC 9110: an invalid weight is ignored, not fatal.
				continue
			}
			quality = parsed
		}
	}
	if value == "" {
		return "", 0, false
	}
	return strings.ToLower(value), quality, true
}

// assetEntry caches the compressed form of one embedded asset. A nil data
// slice records that the asset is not worth compressing, so the decision is
// made once per file and per process.
type assetEntry struct {
	once sync.Once
	data []byte
}

// assetCache holds compressed variants of the immutable embedded assets. Only
// files under the content-addressed asset prefix are cached: they are fixed
// for the lifetime of the process, so the cache cannot grow past the files of
// the embedded build.
type assetCache struct {
	mu      sync.Mutex
	entries map[string]*assetEntry
}

func newAssetCache() *assetCache {
	return &assetCache{entries: make(map[string]*assetEntry)}
}

// compressed returns the gzip form of name, or nil when the asset should be
// served as-is.
func (c *assetCache) compressed(fsys fs.FS, name string) []byte {
	c.mu.Lock()
	entry := c.entries[name]
	if entry == nil {
		entry = &assetEntry{}
		c.entries[name] = entry
	}
	c.mu.Unlock()
	entry.once.Do(func() {
		entry.data = gzipAsset(fsys, name)
	})
	return entry.data
}

// gzipAsset reads an embedded asset and returns its compressed form. It
// returns nil for assets that are too small, of a non-compressible type, or
// that do not shrink.
func gzipAsset(fsys fs.FS, name string) []byte {
	if !compressibleExt[strings.ToLower(path.Ext(name))] {
		return nil
	}
	raw, err := fs.ReadFile(fsys, name)
	if err != nil || len(raw) < gzipMinSize {
		return nil
	}
	var buf bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := writer.Write(raw); err != nil {
		return nil
	}
	if err := writer.Close(); err != nil {
		return nil
	}
	if buf.Len() >= len(raw) {
		return nil
	}
	return buf.Bytes()
}

// serveCompressedAsset answers name with its cached gzip representation when
// the client asked for gzip and the request can be served whole. It returns
// false when the caller must fall back to the plain file server.
func (s *Server) serveCompressedAsset(c *gin.Context, fsys fs.FS, name string) bool {
	// HEAD is left to http.FileServer so its Content-Length handling stays
	// intact, and ranges are never mixed with a content encoding here.
	if c.Request.Method != http.MethodGet {
		return false
	}
	if c.Request.Header.Get("Range") != "" || c.Request.Header.Get("If-Range") != "" {
		return false
	}
	if !strings.HasPrefix(name, cachedAssetPrefix) || !fs.ValidPath(name) {
		return false
	}
	if !compressibleExt[strings.ToLower(path.Ext(name))] {
		return false
	}
	if !acceptsGzip(c.Request.Header.Values("Accept-Encoding")) {
		return false
	}
	data := s.assets.compressed(fsys, name)
	if data == nil {
		return false
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	c.Header("Content-Encoding", "gzip")
	c.Header("Vary", "Accept-Encoding")
	// Set the length explicitly: the body is usually larger than net/http's
	// write buffer, so it would otherwise be chunked. A known length avoids the
	// chunked framing and lets clients report progress.
	c.Header("Content-Length", strconv.Itoa(len(data)))
	c.Data(http.StatusOK, contentType, data)
	return true
}
