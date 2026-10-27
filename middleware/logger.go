package middleware

import (
	"context"
	"log/slog"
	"net/http"
)

// LoggerOptions configures Logger.
type LoggerOptions struct {
	// Logger receives one record per request. Default slog.Default().
	Logger *slog.Logger

	// Level is the level of the records. Default slog.LevelInfo.
	Level slog.Level

	// OmitQuery logs the path without the query string, which may carry
	// tokens or personal data, under the same "uri" key.
	OmitQuery bool

	// Attrs, when set, returns attributes to add to the record of a
	// request, such as the name of the route that served it
	// (router.MatchedRoute).
	Attrs func(*http.Request, ResponseInfo) []slog.Attr
}

// Logger returns middleware that logs every request when it is done, with its
// method, URI, status, response size, duration, client IP (see ClientIP) and
// request ID (see RequestID). A request whose handler panicked is logged as
// well, with panicked=true and status 500 when no status was written, before
// the panic goes on.
func Logger(options ...LoggerOptions) func(http.Handler) http.Handler {
	opts := firstOption(options)

	return Observe(func(r *http.Request, info ResponseInfo) {
		uri := r.URL.RequestURI()
		if opts.OmitQuery {
			uri = r.URL.EscapedPath()
		}

		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("uri", uri),
			slog.Int("status", info.Status),
			slog.Int64("bytes", info.Bytes),
			slog.Duration("duration", info.Duration),
			slog.String("client_ip", ClientIP(r)),
		}

		if info.Panicked {
			attrs = append(attrs, slog.Bool("panicked", true))
		}

		if requestID := GetRequestID(r.Context()); requestID != "" {
			attrs = append(attrs, slog.String("request_id", requestID))
		}

		if opts.Attrs != nil {
			attrs = append(attrs, opts.Attrs(r, info)...)
		}

		loggerOrDefault(opts.Logger).LogAttrs(r.Context(), opts.Level, "request", attrs...)
	})
}

// LogHandler wraps a slog handler so that every record logged with the
// context of a request carries the request's ID (see RequestID) as
// request_id, also records of code that knows nothing about requests:
//
//	logger := slog.New(middleware.LogHandler(slog.NewJSONHandler(os.Stderr, nil)))
//	logger.InfoContext(req.Context(), "page saved") // ... request_id=...
//
// Records that name a request_id already, such as those of Logger and
// Recover, keep theirs. Wrapping a handler twice wraps it once.
func LogHandler(handler slog.Handler) slog.Handler {
	if _, ok := handler.(logHandler); ok {
		return handler
	}

	return logHandler{Handler: handler}
}

type logHandler struct {
	slog.Handler
}

func (h logHandler) Handle(ctx context.Context, record slog.Record) error {
	if id := GetRequestID(ctx); id != "" && !hasRequestID(record) {
		record.AddAttrs(slog.String("request_id", id))
	}

	return h.Handler.Handle(ctx, record)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{Handler: h.Handler.WithGroup(name)}
}

// hasRequestID reports whether the record names a request ID itself.
func hasRequestID(record slog.Record) bool {
	found := false

	record.Attrs(func(attr slog.Attr) bool {
		found = attr.Key == "request_id"

		return !found
	})

	return found
}

// loggerOrDefault returns logger, or slog.Default() when it is nil: the
// logger of middleware whose options leave it out.
func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}

	return logger
}

// firstOption returns the first of options, or the zero options when there
// are none: the options of middleware that takes them as an optional
// argument.
func firstOption[T any](options []T) T {
	var opts T
	if len(options) > 0 {
		opts = options[0]
	}

	return opts
}
