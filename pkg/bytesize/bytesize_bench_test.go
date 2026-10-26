package bytesize_test

import (
	"testing"

	"github.com/blackbirdworks/loki-prune/pkg/bytesize"
)

func BenchmarkParse(b *testing.B) {
	inputs := []string{"100B", "10KiB", "500MB", "48GB", "1.5GiB", "2TiB"}
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		for _, in := range inputs {
			_, _ = bytesize.Parse(in)
		}
	}
}

func BenchmarkFormat(b *testing.B) {
	sizes := []int64{512, 10240, 52428800, 51539607552, 2199023255552}
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		for _, sz := range sizes {
			_ = bytesize.Format(sz)
		}
	}
}
