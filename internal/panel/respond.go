package panel

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
)

// gzipJSONMinSize is the smallest JSON body worth compressing; below it the
// gzip framing costs more than it saves.
const gzipJSONMinSize = 4096

// jsonWriter serializes JSON responses and can gzip them. The compressor is
// pooled because a large response (a client list of a few thousand rows) would
// otherwise allocate a window and a buffer on every request.
type jsonWriter struct {
	pool sync.Pool
}

func newJSONWriter() *jsonWriter {
	return &jsonWriter{
		pool: sync.Pool{
			New: func() any {
				return gzip.NewWriter(io.Discard)
			},
		},
	}
}

// gzipJSON compresses body, returning nil when the compressed form is not
// smaller than the original.
func (w *jsonWriter) gzipJSON(body []byte) []byte {
	writer := w.pool.Get().(*gzip.Writer)
	defer w.pool.Put(writer)

	var buf bytes.Buffer
	// BestSpeed: the panel only compresses a few list responses and the size
	// difference against BestCompression is small, while encode latency matters
	// for requests of a few hundred kilobytes.
	writer.Reset(&buf)
	if _, err := writer.Write(body); err != nil {
		return nil
	}
	if err := writer.Close(); err != nil {
		return nil
	}
	if buf.Len() >= len(body) {
		return nil
	}
	return buf.Bytes()
}

// writeJSON writes payload as JSON.
//
// Unlike the rest of the API (which stays uncompressed because its responses
// carry PSKs and subscription tokens, making compression next to secrets a
// BREACH-style risk), this helper is only used by the client list endpoints:
// their payload holds no credentials, so they can be compressed safely.
func (w *jsonWriter) writeJSON(c *gin.Context, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "encode response")
		return
	}
	// The representation depends on Accept-Encoding even when this client gets
	// the plain body, so shared caches must key on it.
	c.Header("Vary", "Accept-Encoding")
	if len(body) >= gzipJSONMinSize && acceptsGzip(c.Request.Header.Values("Accept-Encoding")) {
		if compressed := w.gzipJSON(body); compressed != nil {
			c.Header("Content-Encoding", "gzip")
			c.Header("Content-Length", strconv.Itoa(len(compressed)))
			c.Data(status, "application/json; charset=utf-8", compressed)
			return
		}
	}
	c.Data(status, "application/json; charset=utf-8", body)
}
