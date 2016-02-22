package goheatshrink

import "errors"

var (
	// ErrTruncated is returned when the input stream ended before decompression was complete
	ErrTruncated = errors.New("heatshrink: ran out of input before finishing")
	// ErrBadStateOnClose is returned when the internal state machine was not in a finished state on Close
	ErrBadStateOnClose = errors.New("heatshrink: state machine in bad state on close")

	errNoBitsAvailable  = errors.New("no available bits")
	errOutputBufferFull = errors.New("output buffer full")
)
