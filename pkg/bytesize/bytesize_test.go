package bytesize_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/bytesize"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err      error
		name     string
		input    string
		expected int64
	}{
		{name: "bytes without unit", input: "100", expected: 100, err: nil},
		{name: "bytes with B", input: "100B", expected: 100, err: nil},
		{name: "kilobytes", input: "10KB", expected: 10000, err: nil},
		{name: "kibibytes", input: "10KiB", expected: 10240, err: nil},
		{name: "megabytes", input: "500MB", expected: 500000000, err: nil},
		{name: "mebibytes", input: "500MiB", expected: 500 * 1024 * 1024, err: nil},
		{name: "gigabytes", input: "48GB", expected: 48 * 1000 * 1000 * 1000, err: nil},
		{name: "gibibytes", input: "48GiB", expected: 48 * 1024 * 1024 * 1024, err: nil},
		{name: "terabytes", input: "1TB", expected: 1000000000000, err: nil},
		{name: "tebibytes", input: "1TiB", expected: 1024 * 1024 * 1024 * 1024, err: nil},
		{name: "lowercase kib", input: "5kib", expected: 5120, err: nil},
		{name: "float value", input: "1.5GB", expected: 1500000000, err: nil},
		{name: "invalid string", input: "invalid", expected: 0, err: bytesize.ErrInvalidFormat},
		{name: "unknown unit", input: "100XB", expected: 0, err: bytesize.ErrUnknownUnit},
		{name: "empty input", input: "", expected: 0, err: bytesize.ErrInvalidFormat},
		{name: "float overflow", input: "1e309GB", expected: 0, err: bytesize.ErrInvalidFormat},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := bytesize.Parse(tc.input)
			if tc.err != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, got)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
		input    int64
	}{
		{name: "small bytes", input: 512, expected: "512 B"},
		{name: "kibibytes", input: 2048, expected: "2.00 KiB"},
		{name: "mebibytes", input: 10485760, expected: "10.00 MiB"},
		{name: "gibibytes", input: 10737418240, expected: "10.00 GiB"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, bytesize.Format(tc.input))
		})
	}
}
