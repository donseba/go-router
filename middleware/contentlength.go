package middleware

import (
	"bytes"
	"net/http"
	"strconv"
)

// ContentLengthMiddleware automatically sets the Content-Length header
func ContentLengthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wrap the ResponseWriter
		clw := &contentLengthWriter{
			ResponseWriter: w,
			buffer:         &bytes.Buffer{},
		}

		// Call the next handler with the wrapped ResponseWriter
		next.ServeHTTP(clw, r)

		// Set the Content-Length header
		contentLength := clw.buffer.Len()
		if clw.Header().Get("Content-Length") == "" {
			clw.Header().Set("Content-Length", strconv.Itoa(contentLength))
		}

		// Write the buffered content to the original ResponseWriter
		if !clw.wroteHeader {
			clw.WriteHeader(clw.statusCode)
		}
		w.Write(clw.buffer.Bytes())
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
		clw.ResponseWriter.WriteHeader(statusCode)
	}
}

func (clw *contentLengthWriter) Write(data []byte) (int, error) {
	return clw.buffer.Write(data)
}
