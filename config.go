package goheatshrink

const (
	defaultWindow    uint8 = 8
	defaultLookahead uint8 = 4
)

// Valid values for Config settings
const (
	MinWindow    uint8 = 4
	MaxWindow    uint8 = 15
	MinLookahead uint8 = 3
)

// Config provides options for configuring a heatshrink Reader or Writer
type Config struct {
	// Window specifies the Base 2 log of the size of the sliding window used to find repeating patterns. A larger value allows
	// searches a larger history of the data, potentially compressing more effectively, but will use more memory and processing time.
	// Recommended default: 8 (embedded systems), 10 (elsewhere)
	Window uint8
	// Lookahead specifies the number of bits used for back-reference lengths. A larger value allows longer substitutions, but since
	// all back-references must use window + lookahead bits, larger window or lookahead can be counterproductive if most patterns are
	// small and/or local.
	// Recommended default: 4
	Lookahead uint8
}

// DefaultConfig provides default values for compression and decompression
var DefaultConfig = &Config{Window: defaultWindow, Lookahead: defaultLookahead}
