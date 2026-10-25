// Package logger provides structured logging built on log/slog, carried on the
// context so that callers control verbosity and format rather than the
// application holding global state.
package logger

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

type ctxKey struct{}

// ParseLevel converts a string representation of a log level into an slog.Level.
func ParseLevel(levelStr string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New creates a new *slog.Logger with the specified output, format type, and level.
// Supported log types: "json" and "text" (default).
func New(w io.Writer, logType string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
	}

	if strings.ToLower(strings.TrimSpace(logType)) == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}

	return slog.New(slog.NewTextHandler(w, opts))
}

// Into returns a copy of parent context carrying the logger.
func Into(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext extracts the *slog.Logger from ctx, returning a discard logger if absent.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}

	return slog.New(slog.DiscardHandler)
}

// With returns a copy of ctx carrying the context's logger extended with args.
func With(ctx context.Context, args ...any) context.Context {
	return Into(ctx, FromContext(ctx).With(args...))
}
