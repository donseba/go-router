package middleware

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
)

// RecoverOptions configures RecoverWithOptions.
type RecoverOptions struct {
	// Logger receives the panic, with the request and the stack. Default
	// slog.Default().
	Logger *slog.Logger
}

// Recover turns a panic in a handler into a 500 response and logs it with
// slog.Default(). See RecoverWithOptions.
func Recover(next http.Handler) http.Handler {
	return RecoverWithOptions(RecoverOptions{})(next)
}

// RecoverWithOptions returns middleware that recovers panics in handlers. The
// panic is logged with its stack. When the response has not started yet, a
// 500 Internal Server Error is written; when it has, the connection is aborted
// with http.ErrAbortHandler, so the client sees a broken response rather than
// a truncated one that looks complete. Panics with http.ErrAbortHandler are
// passed on unlogged, as net/http expects.
func RecoverWithOptions(options RecoverOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &recoverResponseWriter{ResponseWriter: w}

			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(recovered)
				}

				logPanic(loggerOrDefault(options.Logger), r, recovered, debug.Stack())

				if rw.started {
					panic(http.ErrAbortHandler)
				}

				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

// logPanic logs a panic recovered from the handler of r, with the request,
// its ID and the stack of the panic.
func logPanic(logger *slog.Logger, r *http.Request, recovered any, stack []byte) {
	attrs := []slog.Attr{
		slog.Any("panic", recovered),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	}

	if requestID := GetRequestID(r.Context()); requestID != "" {
		attrs = append(attrs, slog.String("request_id", requestID))
	}

	attrs = append(attrs, slog.String("stack", string(stack)))
	logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered", attrs...)
}

// recoverResponseWriter tracks whether the response has started, that is,
// whether its status line and headers may have reached the client.
type recoverResponseWriter struct {
	http.ResponseWriter
	started bool
}

func (w *recoverResponseWriter) WriteHeader(statusCode int) {
	if !informational(statusCode) {
		w.started = true
	}

	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *recoverResponseWriter) Write(data []byte) (int, error) {
	w.started = true

	return w.ResponseWriter.Write(data)
}

func (w *recoverResponseWriter) Flush() {
	_ = w.FlushError()
}

// FlushError allows http.ResponseController to flush through this writer.
func (w *recoverResponseWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil {
		w.started = true
	}

	return err
}

func (w *recoverResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.started = true
	}

	return conn, buf, err
}

func (w *recoverResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// informational reports whether statusCode is a 1xx status that does not end
// the header phase, such as 103 Early Hints.
func informational(statusCode int) bool {
	return statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols
}
