package goheatshrink

const (
	defaultWindow    uint8 = 8
	defaultLookahead uint8 = 4
)

// Valid values for Config settings
const (
	// The minimum window to consider when searching for repeated patterns
	MinWindow    uint8 = 4
	// The maximum window to consider when searching for repeated patterns
	MaxWindow    uint8 = 16
	// The minimum number of bits to use in storing back-references
	MinLookahead uint8 = 3
)

type config struct {
	window    uint8
	lookahead uint8
}

// Window specifies the Base 2 log of the size of the sliding window used to find repeating patterns. A larger value allows
// searches a larger history of the data, potentially compressing more effectively, but will use more memory and processing time.
// Recommended default: 8 (embedded systems), 10 (elsewhere)
func Window(window uint8) func(*config) {
	if window < MinWindow {
		window = MinWindow
	} else if window > MaxWindow {
		window = MaxWindow
	}
	return func(c *config) {
		c.window = window
	}
}

// Lookahead specifies the number of bits used for back-reference lengths. A larger value allows longer substitutions, but since
// all back-references must use window + lookahead bits, larger window or lookahead can be counterproductive if most patterns are
// small and/or local.
// Recommended default: 4
func Lookahead(lookahead uint8) func(*config) {
	if lookahead < MinLookahead {
		lookahead = MinLookahead
	}
	return func(c *config) {
		c.lookahead = lookahead
	}
}

