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

type routingStatusInterceptWriter struct {
	http.ResponseWriter

	interceptMap map[int]func() bool
	statusCode   int
	intercepted  bool
}

func (w *routingStatusInterceptWriter) WriteHeader(statusCode int) {
	if w.intercepted {
		return
	}

	w.statusCode = statusCode
	for code, fn := range w.interceptMap {
		if w.intercepted {
			return
		}

		if code == statusCode && fn() {
			w.intercepted = true
			return
		}
	}

	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *routingStatusInterceptWriter) Write(data []byte) (int, error) {
	if w.intercepted {
		return 0, nil
	}

	return w.ResponseWriter.Write(data)
}
