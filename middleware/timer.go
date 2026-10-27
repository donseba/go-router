package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// TimerOptions configures TimerWithOptions.
type TimerOptions struct {
	// Logger receives one record per request. Default slog.Default().
	Logger *slog.Logger

	// Level is the level of the records. Default slog.LevelInfo.
	Level slog.Level
}

// Timer logs how long every request took, with slog.Default(). See
// TimerWithOptions.
func Timer(next http.Handler) http.Handler {
	return TimerWithOptions(TimerOptions{})(next)
}

// TimerWithOptions returns middleware that logs the duration, method and path
// of every request when it is done.
func TimerWithOptions(options TimerOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			next.ServeHTTP(w, r)

			loggerOrDefault(options.Logger).LogAttrs(r.Context(), options.Level, "request timed",
				slog.Duration("duration", time.Since(start)),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			)
		})
	}
}
