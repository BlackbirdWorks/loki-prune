package compressor

// NewTestCompressor creates a Compressor with custom type for testing edge cases.
func NewTestCompressor(cType string) *Compressor {
	return &Compressor{compType: CompressionType(cType)}
}
