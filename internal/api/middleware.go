package api

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// maxBody is the request body limit for POST requests.
const maxBody = 64 << 10

// statusWriter records the status code so the request log can report it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// ReadFrom keeps io.Copy/sendfile-style copies on the underlying writer.
func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestLog logs every request at debug level.
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelDebug
		if p := r.URL.Path; strings.HasPrefix(p, "/file/") || strings.HasPrefix(p, "/sub/") || strings.HasSuffix(p, "/index.m3u8") {
			level = slog.LevelInfo // what the TV fetches is worth seeing in the log view
		}
		slog.Log(r.Context(), level, "http request",
			"remote", r.RemoteAddr,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
		)
	})
}

// lanOnly rejects clients that are not on a loopback, private or link-local network.
func lanOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLAN(r.RemoteAddr) {
			writeText(w, http.StatusForbidden, "Forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLAN reports whether remote ("host:port" or a bare IP, IPv6 zones allowed)
// is loopback, RFC 1918 / unique-local, or link-local.
func isLAN(remote string) bool {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// cors mirrors server.js cors(), and answers preflight requests with 204.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Range, Content-Type")
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimit caps POST bodies at maxBody bytes.
func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}
