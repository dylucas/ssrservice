package panel

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ydong/ssrservice/internal/webui"
)

// TestAcceptsGzip covers the Accept-Encoding negotiation, including quality
// factors, the wildcard, multiple header lines and malformed weights.
func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   bool
	}{
		{"gzip", []string{"gzip"}, true},
		{"x-gzip", []string{"x-gzip"}, true},
		{"gzip with deflate", []string{"gzip, deflate"}, true},
		{"br then gzip", []string{"br;q=1.0, gzip;q=0.8"}, true},
		{"gzip q half", []string{"gzip;q=0.5"}, true},
		{"gzip q zero", []string{"gzip;q=0"}, false},
		{"gzip q zero uppercase", []string{"gzip;Q=0"}, false},
		{"identity only", []string{"identity"}, false},
		{"missing", nil, false},
		{"empty", []string{""}, false},
		{"wildcard", []string{"*"}, true},
		{"wildcard q zero", []string{"*;q=0"}, false},
		{"wildcard low q", []string{"br, *;q=0.1"}, true},
		{"explicit rejection beats wildcard", []string{"gzip;q=0, *"}, false},
		{"multiple lines", []string{"br", "gzip"}, true},
		{"malformed weight falls back to 1", []string{"gzip;q=abc"}, true},
		{"weight above one is ignored", []string{"gzip;q=2"}, true},
		{"spaces", []string{"  gzip  ;  q=0.3  "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acceptsGzip(tc.values); got != tc.want {
				t.Fatalf("acceptsGzip(%q) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
}

// builtAsset returns the name of an embedded asset matching the filter, or
// skips the test when the frontend was not built.
func builtAsset(t *testing.T, filter func(name string, size int) bool) (string, []byte) {
	t.Helper()
	entries, err := fs.ReadDir(webui.FS(), "assets")
	if err != nil {
		t.Skip("frontend not built; run make web first")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := cachedAssetPrefix + entry.Name()
		data, err := fs.ReadFile(webui.FS(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if filter(name, len(data)) {
			return name, data
		}
	}
	t.Skip("no matching asset in the embedded build")
	return "", nil
}

// doRaw issues a request with full control over the headers, which the shared
// helper cannot express (it takes a single value per header).
func (tp *testPanel) doRaw(method, path string, header http.Header) *httptest.ResponseRecorder {
	tp.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	tp.handler.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, data []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

// TestAssetGzipRoundTrip verifies the compressed representation decodes back to
// the embedded bytes and that the caching headers are consistent.
func TestAssetGzipRoundTrip(t *testing.T) {
	name, raw := builtAsset(t, func(name string, size int) bool {
		return strings.HasSuffix(name, ".js") && size > 8*gzipMinSize
	})
	tp := newTestPanel(t)

	header := http.Header{"Accept-Encoding": []string{"gzip, deflate, br"}}
	rec := tp.doRaw(http.MethodGet, "/"+name, header)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("content-encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Fatalf("vary = %q, want it to mention accept-encoding", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("cache-control = %q, want immutable", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("content-length = %q, body = %d", got, rec.Body.Len())
	}
	if rec.Body.Len() >= len(raw) {
		t.Fatalf("compressed body %d is not smaller than %d", rec.Body.Len(), len(raw))
	}
	if decoded := gunzip(t, rec.Body.Bytes()); !bytes.Equal(decoded, raw) {
		t.Fatalf("decompressed asset does not match the embedded file")
	}
}

// TestAssetNoGzipWithoutAcceptEncoding verifies the plain path is untouched.
func TestAssetNoGzipWithoutAcceptEncoding(t *testing.T) {
	name, raw := builtAsset(t, func(name string, size int) bool {
		return strings.HasSuffix(name, ".js") && size > 8*gzipMinSize
	})
	tp := newTestPanel(t)

	rec := tp.doRaw(http.MethodGet, "/"+name, http.Header{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("content-encoding = %q, want none", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("body differs from the embedded file")
	}
	// The representation still varies on Accept-Encoding, so a shared cache
	// must key on it.
	if got := rec.Header().Get("Vary"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Fatalf("vary = %q, want it to mention accept-encoding", got)
	}
}

// TestAssetGzipQZeroFallsBack verifies an explicit rejection is honored.
func TestAssetGzipQZeroFallsBack(t *testing.T) {
	name, raw := builtAsset(t, func(name string, size int) bool {
		return strings.HasSuffix(name, ".js") && size > 8*gzipMinSize
	})
	tp := newTestPanel(t)

	header := http.Header{"Accept-Encoding": []string{"gzip;q=0, *"}}
	rec := tp.doRaw(http.MethodGet, "/"+name, header)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("content-encoding = %q, want none", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("body differs from the embedded file")
	}
}

// TestAssetSmallFileNotCompressed verifies that a file whose gzip form would be
// larger is served as-is even when the client accepts gzip.
func TestAssetSmallFileNotCompressed(t *testing.T) {
	name, raw := builtAsset(t, func(name string, size int) bool {
		return size > 0 && size < gzipMinSize
	})
	tp := newTestPanel(t)

	header := http.Header{"Accept-Encoding": []string{"gzip"}}
	rec := tp.doRaw(http.MethodGet, "/"+name, header)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("small asset %s was compressed", name)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("body differs from the embedded file")
	}
}

// TestAssetRangeBypassesCompression verifies byte ranges keep their unencoded
// semantics.
func TestAssetRangeBypassesCompression(t *testing.T) {
	name, raw := builtAsset(t, func(name string, size int) bool {
		return strings.HasSuffix(name, ".js") && size > 8*gzipMinSize
	})
	tp := newTestPanel(t)

	header := http.Header{
		"Accept-Encoding": []string{"gzip"},
		"Range":           []string{"bytes=0-9"},
	}
	rec := tp.doRaw(http.MethodGet, "/"+name, header)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("range response was compressed")
	}
	if !bytes.Equal(rec.Body.Bytes(), raw[:10]) {
		t.Fatalf("range body = %q, want %q", rec.Body.Bytes(), raw[:10])
	}
}

// TestAssetCompressionCachedAndConcurrent verifies concurrent first requests
// agree byte for byte (run under -race as well).
func TestAssetCompressionCachedAndConcurrent(t *testing.T) {
	name, _ := builtAsset(t, func(name string, size int) bool {
		return strings.HasSuffix(name, ".css") && size > 8*gzipMinSize
	})
	tp := newTestPanel(t)

	const workers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results [][]byte
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			header := http.Header{"Accept-Encoding": []string{"gzip"}}
			rec := tp.doRaw(http.MethodGet, "/"+name, header)
			mu.Lock()
			results = append(results, append([]byte(nil), rec.Body.Bytes()...))
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(results) != workers {
		t.Fatalf("got %d responses", len(results))
	}
	for i := 1; i < len(results); i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("response %d differs from response 0", i)
		}
	}
}

// TestAttachmentRoutesAreNotCompressed verifies the compression path only ever
// touches the hashed asset directory.
func TestAttachmentRoutesAreNotCompressed(t *testing.T) {
	tp := newTestPanel(t)
	header := http.Header{"Accept-Encoding": []string{"gzip"}}

	rec := tp.doRaw(http.MethodGet, "/healthz", header)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("healthz response was compressed")
	}
}

// TestAccessLogSkipsStaticAssets verifies hashed assets are logged at debug
// level so page loads do not bury API requests.
func TestAccessLogSkipsStaticAssets(t *testing.T) {
	name, _ := builtAsset(t, func(name string, size int) bool {
		return strings.HasSuffix(name, ".js") && size > 8*gzipMinSize
	})
	tp := newTestPanel(t)

	var logs bytes.Buffer
	tp.server.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	tp.doRaw(http.MethodGet, "/"+name, http.Header{})
	if strings.Contains(logs.String(), "http request") {
		t.Fatalf("static asset was logged at info level:\n%s", logs.String())
	}

	logs.Reset()
	tp.doRaw(http.MethodGet, "/healthz", http.Header{})
	if !strings.Contains(logs.String(), "http request") {
		t.Fatalf("healthz request was not logged at info level:\n%s", logs.String())
	}
}

// TestAssetGzipIsSkippedForUnbuiltFrontend keeps the "not built" page behavior
// intact.
func TestUnbuiltFrontendServesPlaceholder(t *testing.T) {
	if webui.Built() {
		t.Skip("frontend is built")
	}
	tp := newTestPanel(t)
	rec := tp.doRaw(http.MethodGet, "/", http.Header{"Accept-Encoding": []string{"gzip"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "前端尚未构建") {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

// TestRuntimeConfigStillInjected verifies the compression path did not disturb
// the SPA shell.
func TestRuntimeConfigStillInjected(t *testing.T) {
	if !webui.Built() {
		t.Skip("frontend not built; run make web first")
	}
	tp := newTestPanel(t)
	rec := tp.doRaw(http.MethodGet, "/", http.Header{"Accept-Encoding": []string{"gzip"}})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("index.html must not be compressed, got %q", got)
	}
	body := rec.Body.String()
	start := strings.Index(body, `id="`+runtimeConfigID+`"`)
	if start < 0 {
		t.Fatalf("runtime config block missing")
	}
	var cfg runtimeConfig
	fragment := body[strings.Index(body[start:], ">")+start+1:]
	fragment = fragment[:strings.Index(fragment, "</script>")]
	if err := json.Unmarshal([]byte(fragment), &cfg); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	if cfg.BasePath != "/" {
		t.Fatalf("unexpected base path %q", cfg.BasePath)
	}
}

// BenchmarkAssetGzipCacheHit measures the cost of serving a cached compressed
// asset, which is the steady-state path for dashboard loads.
func BenchmarkAssetGzipCacheHit(b *testing.B) {
	if !webui.Built() {
		b.Skip("frontend not built")
	}
	entries, err := fs.ReadDir(webui.FS(), "assets")
	if err != nil {
		b.Skip("no assets")
	}
	name := ""
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".js") {
			name = cachedAssetPrefix + entry.Name()
			break
		}
	}
	if name == "" {
		b.Skip("no js asset")
	}
	fsys := webui.FS()
	cache := newAssetCache()
	if cache.compressed(fsys, name) == nil {
		b.Skip("asset does not compress")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cache.compressed(fsys, name) == nil {
			b.Fatal("cache miss")
		}
	}
}

// BenchmarkAcceptEncodingParse measures the per-request negotiation cost.
func BenchmarkAcceptEncodingParse(b *testing.B) {
	values := []string{"gzip, deflate, br, zstd"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !acceptsGzip(values) {
			b.Fatal("expected gzip")
		}
	}
}
