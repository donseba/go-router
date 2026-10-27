package middleware

import (
	"bufio"
	"net"
	"net/http"
	"time"
)

// ResponseInfo is what Observe and Logger know about a response once it is
// done.
type ResponseInfo struct {
	// Status is the response status: 200 when the handler wrote a body
	// without a status, 101 for a hijacked connection, and 500 for a
	// request whose handler panicked before it wrote a status.
	Status int

	// Bytes is the size of the body written, before any compression the
	// writer below does.
	Bytes int64

	// Duration is how long the request took.
	Duration time.Duration

	// Panicked is set when a panic passed through, such as
	// http.ErrAbortHandler after the response started; the panic goes on
	// after the observer ran.
	Panicked bool

	// Header is the response's header map as the handler left it: the
	// headers written with the response, such as Location or
	// X-Robots-Tag, and any set after the status was written, which were
	// not sent. It is the writer's own map, not a copy: read it, do not
	// change it.
	Header http.Header
}

// Observe returns middleware that calls observe for every request when it
// is done, with the response's status, size and duration, for metrics and
// access logs:
//
//	r.UseAll(middleware.Observe(func(req *http.Request, info middleware.ResponseInfo) {
//		requests.WithLabelValues(req.Method, strconv.Itoa(info.Status)).Inc()
//	}))
//
// The response headers are in ResponseInfo.Header, for an observer that
// tells responses apart by them, such as one that lists the 404s search
// engines may index:
//
//	middleware.Observe(func(req *http.Request, info middleware.ResponseInfo) {
//		if info.Status == http.StatusNotFound && info.Header.Get("X-Robots-Tag") == "" {
//			missed(req.URL.Path)
//		}
//	})
//
// observe also runs for a request whose handler panicked, before the panic
// goes on, so put Recover inside Observe to answer and count panics as a
// 500. With router.TrackRoutes, router.MatchedRoute(req) tells observe which
// route served the request, and router.MatchedLabel(req) its label without
// copying the route.
func Observe(observe func(*http.Request, ResponseInfo)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observeRequest(w, r, next, observe)
		})
	}
}

// observeRequest serves r through next and calls observe with the result,
// also when a panic passes through.
func observeRequest(w http.ResponseWriter, r *http.Request, next http.Handler, observe func(*http.Request, ResponseInfo)) {
	var (
		started   = time.Now()
		recorder  = &responseRecorder{ResponseWriter: w}
		completed = false
	)

	defer func() {
		info := ResponseInfo{
			Status:   recorder.status,
			Bytes:    recorder.bytes,
			Duration: time.Since(started),
			Panicked: !completed,
			Header:   w.Header(),
		}

		if info.Status == 0 {
			info.Status = http.StatusOK
			if !completed {
				info.Status = http.StatusInternalServerError
			}
		}

		observe(r, info)
	}()

	next.ServeHTTP(recorder, r)

	completed = true
}

// responseRecorder notes the status and size of a response.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseRecorder) WriteHeader(statusCode int) {
	if w.status == 0 && !informational(statusCode) {
		w.status = statusCode
	}

	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)

	return n, err
}

func (w *responseRecorder) Flush() {
	_ = w.FlushError()
}

// FlushError allows http.ResponseController to flush through this writer.
func (w *responseRecorder) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil && w.status == 0 {
		w.status = http.StatusOK
	}

	return err
}

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil && w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}

	return conn, buf, err
}

func (w *responseRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
