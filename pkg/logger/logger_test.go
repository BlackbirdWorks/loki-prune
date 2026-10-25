package logger_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/blackbirdworks/loki-prune/pkg/logger"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected slog.Level
	}{
		{name: "debug", input: "debug", expected: slog.LevelDebug},
		{name: "warn", input: "warn", expected: slog.LevelWarn},
		{name: "warning", input: "warning", expected: slog.LevelWarn},
		{name: "error", input: "error", expected: slog.LevelError},
		{name: "info", input: "info", expected: slog.LevelInfo},
		{name: "unknown defaults to info", input: "unknown", expected: slog.LevelInfo},
		{name: "empty defaults to info", input: "", expected: slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, logger.ParseLevel(tc.input))
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		validate func(t *testing.T, output string)
		name     string
		logType  string
		level    slog.Level
	}{
		{
			name:    "text logger",
			logType: "text",
			level:   slog.LevelInfo,
			validate: func(t *testing.T, output string) {
				t.Helper()
				assert.Contains(t, output, "msg=\"test message\"")
			},
		},
		{
			name:    "json logger",
			logType: "json",
			level:   slog.LevelInfo,
			validate: func(t *testing.T, output string) {
				t.Helper()
				assert.Contains(t, output, `"msg":"test message"`)
			},
		},
		{
			name:    "default fallback to text",
			logType: "other",
			level:   slog.LevelDebug,
			validate: func(t *testing.T, output string) {
				t.Helper()
				assert.Contains(t, output, "msg=\"test message\"")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			l := logger.New(&buf, tc.logType, tc.level)
			l.Info("test message")
			tc.validate(t, buf.String())
		})
	}
}

func TestInto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup func() (context.Context, *slog.Logger)
		name  string
	}{
		{
			name: "standard logger injection",
			setup: func() (context.Context, *slog.Logger) {
				var buf bytes.Buffer

				return context.Background(), logger.New(&buf, "text", slog.LevelInfo)
			},
		},
		{
			name: "nil logger injection",
			setup: func() (context.Context, *slog.Logger) {
				return context.Background(), nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, l := tc.setup()
			resCtx := logger.Into(ctx, l)
			assert.NotNil(t, resCtx)
		})
	}
}

func TestFromContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup func() (context.Context, func(t *testing.T, l *slog.Logger))
		name  string
	}{
		{
			name: "nil context returns discard logger",
			setup: func() (context.Context, func(t *testing.T, l *slog.Logger)) {
				var nilCtx context.Context

				return nilCtx, func(t *testing.T, l *slog.Logger) {
					t.Helper()
					assert.NotNil(t, l)
				}
			},
		},
		{
			name: "empty context without logger returns discard logger",
			setup: func() (context.Context, func(t *testing.T, l *slog.Logger)) {
				return context.Background(), func(t *testing.T, l *slog.Logger) {
					t.Helper()
					assert.NotNil(t, l)
				}
			},
		},
		{
			name: "context with injected logger retrieves same logger",
			setup: func() (context.Context, func(t *testing.T, l *slog.Logger)) {
				var buf bytes.Buffer
				orig := logger.New(&buf, "text", slog.LevelInfo)
				ctx := logger.Into(context.Background(), orig)

				return ctx, func(t *testing.T, l *slog.Logger) {
					t.Helper()
					l.Info("from context")
					assert.Contains(t, buf.String(), "from context")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, verify := tc.setup()
			l := logger.FromContext(ctx)
			verify(t, l)
		})
	}
}

func TestWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		attrs []any
	}{
		{
			name:  "adds attributes to context logger",
			attrs: []any{"component", "tester"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			base := logger.New(&buf, "text", slog.LevelInfo)
			ctx := logger.Into(context.Background(), base)

			ctxWith := logger.With(ctx, tc.attrs...)
			logger.FromContext(ctxWith).Info("test")
			assert.Contains(t, buf.String(), "component=tester")
		})
	}
}
