// Package bytesize provides zero-allocation parsing and formatting of human-friendly byte strings.
package bytesize

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// ErrInvalidFormat is returned when a byte size string is empty or contains non-alphanumeric syntax.
	ErrInvalidFormat = errors.New("invalid size format")
	// ErrUnknownUnit is returned when the byte unit is unrecognized.
	ErrUnknownUnit = errors.New("unknown unit")
)

const (
	// Byte represents 1 byte.
	Byte = 1
	// Kilo represents 1,000 bytes.
	Kilo = 1000
	// Kibi represents 1,024 bytes.
	Kibi = 1024
	// Mega represents 1,000,000 bytes.
	Mega = Kilo * 1000
	// Mebi represents 1,048,576 bytes.
	Mebi = Kibi * 1024
	// Giga represents 1,000,000,000 bytes.
	Giga = Mega * 1000
	// Gibi represents 1,073,741,824 bytes.
	Gibi = Mebi * 1024
	// Tera represents 1,000,000,000,000 bytes.
	Tera = Giga * 1000
	// Tebi represents 1,099,511,627,776 bytes.
	Tebi = Gibi * 1024
	// Peta represents 1,000,000,000,000,000 bytes.
	Peta = Tera * 1000
	// Pebi represents 1,125,899,906,842,624 bytes.
	Pebi = Tebi * 1024

	base10         = 10
	floatPrecision = 2
	bitSize64      = 64
	unitLen1       = 1
	unitLen2       = 2
	unitLen3       = 3
)

// Parse converts a human-readable size string (e.g. "48GB", "500MiB", "10.5KB") into bytes.
func Parse(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("%w: %q", ErrInvalidFormat, s)
	}

	i := 0
	hasDot := false
	for i < len(trimmed) {
		c := trimmed[i]
		switch {
		case c >= '0' && c <= '9':
			i++
		case c == '.' && !hasDot:
			hasDot = true
			i++
		default:
			goto parseNumber
		}
	}

parseNumber:
	if i == 0 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidFormat, s)
	}

	val, err := strconv.ParseFloat(trimmed[:i], bitSize64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrInvalidFormat, trimmed[:i], err)
	}

	unitStr := strings.TrimSpace(trimmed[i:])
	if !validateUnitString(unitStr) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidFormat, s)
	}

	mult, err := multiplier(unitStr)
	if err != nil {
		return 0, err
	}

	return int64(val * mult), nil
}

func validateUnitString(unitStr string) bool {
	for j := range len(unitStr) {
		c := unitStr[j]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}

	return true
}

func multiplier(unit string) (float64, error) {
	switch len(unit) {
	case 0:
		return 1, nil
	case unitLen1:
		return singleCharMultiplier(unit[0])
	case unitLen2:
		return twoCharMultiplier(unit)
	case unitLen3:
		return threeCharMultiplier(unit)
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnknownUnit, unit)
	}
}

func singleCharMultiplier(c byte) (float64, error) {
	switch c {
	case 'b', 'B':
		return Byte, nil
	case 'k', 'K':
		return Kilo, nil
	case 'm', 'M':
		return Mega, nil
	case 'g', 'G':
		return Giga, nil
	case 't', 'T':
		return Tera, nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnknownUnit, string(c))
	}
}

func twoCharMultiplier(unit string) (float64, error) {
	switch {
	case strings.EqualFold(unit, "KB"):
		return Kilo, nil
	case strings.EqualFold(unit, "MB"):
		return Mega, nil
	case strings.EqualFold(unit, "GB"):
		return Giga, nil
	case strings.EqualFold(unit, "TB"):
		return Tera, nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnknownUnit, unit)
	}
}

func threeCharMultiplier(unit string) (float64, error) {
	switch {
	case strings.EqualFold(unit, "KiB"):
		return Kibi, nil
	case strings.EqualFold(unit, "MiB"):
		return Mebi, nil
	case strings.EqualFold(unit, "GiB"):
		return Gibi, nil
	case strings.EqualFold(unit, "TiB"):
		return Tebi, nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnknownUnit, unit)
	}
}

// Format formats a raw byte count into a human-friendly string (e.g. "48.25 GiB").
func Format(bytes int64) string {
	if bytes < Kibi {
		var buf [24]byte
		b := strconv.AppendInt(buf[:0], bytes, base10)
		b = append(b, ' ', 'B')

		return string(b)
	}
	div, exp := int64(Kibi), 0
	for n := bytes / Kibi; n >= Kibi; n /= Kibi {
		div *= Kibi
		exp++
	}

	val := float64(bytes) / float64(div)
	var buf [32]byte
	b := strconv.AppendFloat(buf[:0], val, 'f', floatPrecision, bitSize64)
	b = append(b, ' ', "KMGTPE"[exp], 'i', 'B')

	return string(b)
}
