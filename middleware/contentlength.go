package middleware

import (
	"bytes"
	"net/http"
	"strconv"
)

// ContentLengthMiddleware automatically sets the Content-Length header
func ContentLengthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clw := &contentLengthWriter{
			ResponseWriter: w,
			buffer:         &bytes.Buffer{},
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(clw, r)

		if shouldSetContentLength(r.Method, clw.statusCode) && clw.Header().Get("Content-Length") == "" {
			clw.Header().Set("Content-Length", strconv.Itoa(clw.buffer.Len()))
		}

		w.WriteHeader(clw.statusCode)
		if r.Method != http.MethodHead && clw.statusCode != http.StatusNoContent && clw.statusCode != http.StatusNotModified {
			_, _ = w.Write(clw.buffer.Bytes())
		}
	})
}

type contentLengthWriter struct {
	http.ResponseWriter
	buffer      *bytes.Buffer
	statusCode  int
	wroteHeader bool
}

func (clw *contentLengthWriter) WriteHeader(statusCode int) {
	if !clw.wroteHeader {
		clw.statusCode = statusCode
		clw.wroteHeader = true
	}
}

func (clw *contentLengthWriter) Write(data []byte) (int, error) {
	return clw.buffer.Write(data)
}

func (clw *contentLengthWriter) Unwrap() http.ResponseWriter {
	return clw.ResponseWriter
}

func shouldSetContentLength(method string, statusCode int) bool {
	if method == http.MethodHead {
		return true
	}

	return statusCode != http.StatusNotModified
}
