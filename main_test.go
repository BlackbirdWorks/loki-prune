package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunMain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		expectedCode int
	}{
		{
			name:         "help returns zero exit code",
			args:         []string{"--help"},
			expectedCode: 0,
		},
		{
			name:         "invalid argument returns exit code 1",
			args:         []string{"--invalid-flag"},
			expectedCode: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code := runMain(tc.args)
			assert.Equal(t, tc.expectedCode, code)
		})
	}
}
