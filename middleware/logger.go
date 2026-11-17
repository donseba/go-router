package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
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
// Records also carry the attributes WithLogAttrs put in their context, such
// as the site of a process that serves several. Records that name a key
// already, such as the request_id of Logger and Recover, keep their own
// value. Wrapping a handler twice wraps it once.
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
	if id := GetRequestID(ctx); id != "" && !hasKey(record, "request_id") {
		record.AddAttrs(slog.String("request_id", id))
	}

	for _, attr := range LogAttrs(ctx) {
		if !hasKey(record, attr.Key) {
			record.AddAttrs(attr)
		}
	}

	return h.Handler.Handle(ctx, record)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{Handler: h.Handler.WithGroup(name)}
}

// hasKey reports whether the record names key itself.
func hasKey(record slog.Record, key string) bool {
	found := false

	record.Attrs(func(attr slog.Attr) bool {
		found = attr.Key == key

		return !found
	})

	return found
}

type logAttrsContextKey struct{}

// WithLogAttrs returns ctx carrying attrs after those it carries already.
// LogHandler adds them to every record logged with ctx, or with a context
// made from it, such as that of a request or of background work:
//
//	ctx = middleware.WithLogAttrs(ctx, slog.String("site", "example.com"))
//	logger.InfoContext(ctx, "page saved") // ... site=example.com
//
// A process that serves several sites puts the site in the context of each
// request and of each site's background work, so their records can be told
// apart without passing a logger around.
func WithLogAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}

	carried := append(slices.Clip(LogAttrs(ctx)), attrs...)

	return context.WithValue(ctx, logAttrsContextKey{}, carried)
}

// LogAttrs returns the attributes WithLogAttrs put in ctx, oldest first, or
// nil.
func LogAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}

	attrs, _ := ctx.Value(logAttrsContextKey{}).([]slog.Attr)

	return attrs
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
