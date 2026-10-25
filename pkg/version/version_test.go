package version_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/blackbirdworks/loki-prune/pkg/version"
)

func TestVersionDefaults(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, version.BuildVersion)
	assert.NotEmpty(t, version.Commit)
	assert.NotEmpty(t, version.BuildDate)
}
