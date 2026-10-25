package version

var (
	// BuildVersion is set by ldflags at build time.
	BuildVersion = "dev"
	// Commit is set by ldflags at build time.
	Commit = "none"
	// BuildDate is set by ldflags at build time.
	BuildDate = "unknown"
)
