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
			method:         r.Method,
		}

		next.ServeHTTP(clw, r)
		if clw.streaming {
			return
		}

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
	method      string
	streaming   bool
}

func (clw *contentLengthWriter) WriteHeader(statusCode int) {
	if !clw.wroteHeader {
		clw.statusCode = statusCode
		clw.wroteHeader = true
	}
}

func (clw *contentLengthWriter) Write(data []byte) (int, error) {
	if !clw.wroteHeader {
		clw.WriteHeader(http.StatusOK)
	}
	if clw.streaming {
		if !clw.writeBody() {
			return len(data), nil
		}
		return clw.ResponseWriter.Write(data)
	}
	return clw.buffer.Write(data)
}

// Flush switches to streaming, writes buffered data, and leaves Content-Length
// unset unless the handler supplied it. Subsequent writes go directly through.
func (clw *contentLengthWriter) Flush() {
	_ = clw.FlushError()
}

// FlushError allows http.ResponseController to flush through this middleware.
func (clw *contentLengthWriter) FlushError() error {
	if !clw.streaming {
		if !clw.wroteHeader {
			clw.WriteHeader(http.StatusOK)
		}
		clw.streaming = true
		clw.ResponseWriter.WriteHeader(clw.statusCode)
	}
	if clw.writeBody() {
		if _, err := clw.buffer.WriteTo(clw.ResponseWriter); err != nil {
			return err
		}
	} else {
		clw.buffer.Reset()
	}
	return http.NewResponseController(clw.ResponseWriter).Flush()
}

func (clw *contentLengthWriter) writeBody() bool {
	return clw.method != http.MethodHead && clw.statusCode != http.StatusNoContent && clw.statusCode != http.StatusNotModified
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
