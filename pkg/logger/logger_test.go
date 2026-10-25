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

	type testCase struct {
		name     string
		input    string
		expected slog.Level
	}

	tests := []testCase{
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

	type testCase struct {
		validate func(t *testing.T, output string)
		name     string
		logType  string
		level    slog.Level
	}

	tests := []testCase{
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

func TestContextPropagation(t *testing.T) {
	t.Parallel()

	t.Run("nil context returns discard logger", func(t *testing.T) {
		t.Parallel()
		var nilCtx context.Context
		l := logger.FromContext(nilCtx)
		assert.NotNil(t, l)
	})

	t.Run("context without logger returns discard logger", func(t *testing.T) {
		t.Parallel()
		l := logger.FromContext(context.Background())
		assert.NotNil(t, l)
	})

	t.Run("inject and extract logger", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		baseLogger := logger.New(&buf, "text", slog.LevelInfo)
		ctx := logger.Into(context.Background(), baseLogger)

		retrieved := logger.FromContext(ctx)
		retrieved.Info("hello from context")
		assert.Contains(t, buf.String(), "hello from context")
	})

	t.Run("with adds attributes", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		baseLogger := logger.New(&buf, "text", slog.LevelInfo)
		ctx := logger.Into(context.Background(), baseLogger)

		ctxWithAttr := logger.With(ctx, "service", "loki-prune")
		logger.FromContext(ctxWithAttr).Info("operational")
		assert.Contains(t, buf.String(), "service=loki-prune")
	})
}
