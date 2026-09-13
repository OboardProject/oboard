package controller

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Response compression for the panel surface.
//
// Nothing was compressed before this. A cold panel load moved about 2.9 MB of
// JavaScript and CSS and about 2 MB of JSON, all in the clear, and the two
// heaviest API responses were re-fetched on every poll. This is repetitive
// JSON and minified source, which is exactly what gzip is good at.
//
// What is deliberately NOT compressed:
//
//   - The Agent wire (/api/v1/agent/) and the relay wire. Those are a versioned
//     contract with a separate implementation; changing their transfer encoding
//     is not a panel optimisation and is not in this change's scope.
//   - Subscriptions. Their clients are third-party proxy applications, and the
//     requested output format is a contract we preserve rather than negotiate.
//   - Anything hijacked (WebSocket) or streamed as text/event-stream.
//   - Range requests, where a compressed body would not be the range asked for.
//
// Bodies below compressionThreshold are passed through: below roughly a packet
// there is nothing to win and gzip's own header would make some of them larger.

const compressionThreshold = 1024

// compressiblePaths are the prefixes this middleware may compress. Everything
// else is passed through untouched, including the Agent and subscription wires.
func compressiblePath(path string) bool {
	if strings.HasPrefix(path, "/api/") {
		return strings.HasPrefix(path, "/api/v1/ui/")
	}
	// "/s" and "/s/..." are the short subscription routes, served to the same
	// third-party clients as the long ones.
	if path == "/s" || strings.HasPrefix(path, "/s/") {
		return false
	}
	// The SPA and its assets.
	return true
}

func compressibleContentType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	switch value {
	case "application/json", "application/javascript", "application/manifest+json",
		"image/svg+xml", "application/wasm", "text/html", "text/css", "text/plain",
		"text/javascript", "text/xml", "application/xml":
		return true
	}
	return false
}

var gzipWriterPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

func (s *Server) withCompression(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !clientAcceptsGzip(r) || r.Header.Get("Range") != "" || !compressiblePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Vary is set whether or not this particular response ends up
		// compressed: the decision depends on the request header, so a cache
		// must key on it either way.
		w.Header().Add("Vary", "Accept-Encoding")
		writer := &compressingWriter{ResponseWriter: w}
		defer writer.Close()
		next.ServeHTTP(writer, r)
	})
}

func clientAcceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return true
		}
	}
	return false
}

// compressingWriter decides on the first write whether the response is worth
// compressing, so a handler that sets its content type and then writes a small
// body is passed through untouched.
type compressingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	decided     bool
	gz          *gzip.Writer
	buffered    []byte
}

func (w *compressingWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	// The header is not sent yet: the compression decision may still add
	// Content-Encoding and drop Content-Length. It is flushed by decide().
}

func (w *compressingWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.decided {
		if w.gz != nil {
			return w.gz.Write(data)
		}
		return w.ResponseWriter.Write(data)
	}
	w.buffered = append(w.buffered, data...)
	if len(w.buffered) < compressionThreshold {
		return len(data), nil
	}
	if err := w.decide(true); err != nil {
		return 0, err
	}
	return len(data), nil
}

// decide commits to compressed or plain and flushes whatever was buffered.
func (w *compressingWriter) decide(compress bool) error {
	if w.decided {
		return nil
	}
	w.decided = true
	header := w.ResponseWriter.Header()
	if compress && header.Get("Content-Encoding") == "" && compressibleContentType(header.Get("Content-Type")) &&
		!strings.EqualFold(header.Get("Content-Type"), "text/event-stream") {
		header.Set("Content-Encoding", "gzip")
		// The length of the identity body is no longer the length of what is
		// sent, and the compressed length is not known until the body ends.
		header.Del("Content-Length")
		gz, _ := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w.ResponseWriter)
		w.gz = gz
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buffered) == 0 {
		return nil
	}
	body := w.buffered
	w.buffered = nil
	var err error
	if w.gz != nil {
		_, err = w.gz.Write(body)
	} else {
		_, err = w.ResponseWriter.Write(body)
	}
	return err
}

func (w *compressingWriter) Close() {
	if !w.wroteHeader && len(w.buffered) == 0 && !w.decided {
		// The handler wrote nothing at all; leave the response untouched so a
		// hijacked or empty response is not given a header it never had.
		return
	}
	// A body that never reached the threshold is sent as it is.
	_ = w.decide(len(w.buffered) >= compressionThreshold)
	if w.gz != nil {
		_ = w.gz.Close()
		gzipWriterPool.Put(w.gz)
		w.gz = nil
	}
}

// Flush sends what is buffered. A handler that flushes is streaming, so the
// decision cannot wait for the threshold any longer.
func (w *compressingWriter) Flush() {
	if !w.decided {
		_ = w.decide(compressibleContentType(w.ResponseWriter.Header().Get("Content-Type")) &&
			!strings.EqualFold(w.ResponseWriter.Header().Get("Content-Type"), "text/event-stream"))
	}
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack passes the connection through untouched. A hijacked response never
// goes through Write, so nothing has been compressed at this point.
func (w *compressingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	w.decided = true
	w.buffered = nil
	return hijacker.Hijack()
}

func (w *compressingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
