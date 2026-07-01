package middleware

import (
	"log"
	"net/http"
	"time"
)

type LoggerOptions struct {
	Logger *log.Logger
}

func Logger(options ...LoggerOptions) func(http.Handler) http.Handler {
	var logger *log.Logger
	if len(options) > 0 {
		logger = options[0].Logger
	}
	if logger == nil {
		logger = log.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			lrw := &loggingResponseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			next.ServeHTTP(lrw, r)

			logger.Printf("[go-router] %s %s %d %d %s %s",
				r.Method,
				r.URL.RequestURI(),
				lrw.statusCode,
				lrw.bytesWritten,
				time.Since(start),
				GetRequestID(r.Context()),
			)
		})
	}
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode    int
	bytesWritten  int
	headerWritten bool
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	if w.headerWritten {
		return
	}

	w.statusCode = statusCode
	w.headerWritten = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}

	n, err := w.ResponseWriter.Write(data)
	w.bytesWritten += n
	return n, err
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
