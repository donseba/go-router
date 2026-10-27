package router

import (
	"bufio"
	"net"
	"net/http"
)

// HeaderFlagDoNotIntercept is a response header that keeps a response from
// being replaced by a HandleStatus handler. The router removes it before the
// response is sent. NoIntercept does the same without a header.
const HeaderFlagDoNotIntercept = "do_not_intercept"

// NoIntercept keeps the response written to w from being replaced by the
// handlers of HandleStatus, for a handler that writes its own error page:
//
//	func page(w http.ResponseWriter, req *http.Request) {
//		if missing {
//			router.NoIntercept(w)
//			w.WriteHeader(http.StatusNotFound)
//			renderNotFound(w, req)
//			return
//		}
//		...
//	}
//
// Call it before the status is written. It reaches the router through
// response writers that implement Unwrap() http.ResponseWriter, as the
// writers of this package's middleware and http.ResponseController-aware
// wrappers do, and applies to every router on the way, also routers a
// mounted router is mounted on. For a wrapper without Unwrap, set the
// HeaderFlagDoNotIntercept header instead. It does nothing when no status
// handlers are registered.
func NoIntercept(w http.ResponseWriter) {
	for w != nil {
		if interceptor, ok := w.(*routingStatusInterceptWriter); ok {
			interceptor.keep = true
		}

		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}

		w = unwrapper.Unwrap()
	}
}

// excludeHeaderWriter removes internal headers before the response headers
// are sent, whichever way that happens: WriteHeader, Write or Flush.
type excludeHeaderWriter struct {
	http.ResponseWriter

	excludedHeaders []string
	excluded        bool
}

func (w *excludeHeaderWriter) exclude() {
	if w.excluded {
		return
	}

	w.excluded = true
	for _, header := range w.excludedHeaders {
		w.Header().Del(header)
	}
}

func (w *excludeHeaderWriter) WriteHeader(statusCode int) {
	w.exclude()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *excludeHeaderWriter) Write(data []byte) (int, error) {
	w.exclude()

	return w.ResponseWriter.Write(data)
}

func (w *excludeHeaderWriter) Flush() {
	_ = w.FlushError()
}

// FlushError allows http.ResponseController to flush through this writer.
func (w *excludeHeaderWriter) FlushError() error {
	w.exclude()

	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *excludeHeaderWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *excludeHeaderWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// routingStatusInterceptWriter swallows a response whose status has a handler
// registered with HandleStatus, so that the handler can answer instead. Once
// the response has started, it is passed through untouched.
type routingStatusInterceptWriter struct {
	http.ResponseWriter

	statusHandlers map[int]http.HandlerFunc
	statusCode     int
	intercepted    bool
	wroteHeader    bool
	keep           bool // NoIntercept: pass the response through
}

func (w *routingStatusInterceptWriter) WriteHeader(statusCode int) {
	if w.intercepted {
		return
	}

	if w.wroteHeader || statusCode < http.StatusOK {
		w.ResponseWriter.WriteHeader(statusCode)

		return
	}

	w.statusCode = statusCode
	if handler := w.statusHandlers[statusCode]; handler != nil && !w.keep && w.Header().Get(HeaderFlagDoNotIntercept) == "" {
		w.intercepted = true

		// The status handler writes its own body; do not let it inherit the
		// type and length of the response it replaces.
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")

		return
	}

	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *routingStatusInterceptWriter) Write(data []byte) (int, error) {
	if w.intercepted {
		return len(data), nil
	}

	w.wroteHeader = true

	return w.ResponseWriter.Write(data)
}

func (w *routingStatusInterceptWriter) Flush() {
	_ = w.FlushError()
}

// FlushError allows http.ResponseController to flush through this writer. A
// flush of an intercepted response is dropped, since flushing would send its
// status before the status handler runs.
func (w *routingStatusInterceptWriter) FlushError() error {
	if w.intercepted {
		return nil
	}

	w.wroteHeader = true

	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *routingStatusInterceptWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.wroteHeader = true
	}

	return conn, buf, err
}

func (w *routingStatusInterceptWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
