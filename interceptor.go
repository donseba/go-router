package router

import "net/http"

// HeaderFlagDoNotIntercept defines a header that is (unfortunately) to be used
// as a flag of sorts, to denote to this routing engine to not intercept the
// response that is being written. It's an unfortunate artifact of an
// implementation detail within the standard library's net/http.ServeMux for how
// HTTP 404 and 405 responses can be customized, which requires writing a custom
// response writer and preventing the standard library from just writing it's
// own hard-coded response.
//
// See:
//   - https://github.com/golang/go/issues/10123
//   - https://github.com/golang/go/issues/21548
//   - https://github.com/golang/go/issues/65648
//
// Author : https://github.com/Rican7 ( https://github.com/golang/go/issues/65648#issuecomment-2100088200 )
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

	intercept404 func() bool
	intercept405 func() bool
	intercept500 func() bool

	statusCode  int
	intercepted bool
}

func (w *routingStatusInterceptWriter) WriteHeader(statusCode int) {
	if w.intercepted {
		return
	}

	w.statusCode = statusCode

	if (w.intercept404() && statusCode == http.StatusNotFound) ||
		(w.intercept405() && statusCode == http.StatusMethodNotAllowed) ||
		(w.intercept500() && statusCode == http.StatusInternalServerError) {

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
