package router

import (
	"net/http"
)

const HeaderFlagDoNotIntercept = "do_not_intercept"

type excludeHeaderWriter struct {
	http.ResponseWriter

	excludedHeaders []string
}

func (w *excludeHeaderWriter) WriteHeader(statusCode int) {
	for _, header := range w.excludedHeaders {
		w.Header().Del(header)
	}

	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *excludeHeaderWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type routingStatusInterceptWriter struct {
	http.ResponseWriter

	statusHandlers map[int]http.HandlerFunc
	statusCode     int
	intercepted    bool
}

func (w *routingStatusInterceptWriter) WriteHeader(statusCode int) {
	if w.intercepted {
		return
	}

	w.statusCode = statusCode
	if handler := w.statusHandlers[statusCode]; handler != nil && w.Header().Get(HeaderFlagDoNotIntercept) == "" {
		w.intercepted = true
		return
	}

	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *routingStatusInterceptWriter) Write(data []byte) (int, error) {
	if w.intercepted {
		return 0, nil
	}

	return w.ResponseWriter.Write(data)
}

func (w *routingStatusInterceptWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
