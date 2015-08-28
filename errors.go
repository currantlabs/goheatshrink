package goheatshrink

import "errors"

var (
	// ErrTruncated is returned when the input stream ended before decompression was complete
	ErrTruncated = errors.New("heatshrink: ran out of input before finishing")
	// ErrBadStateOnClose is returned when the internal state machine was not in a finished state on Close
	ErrBadStateOnClose = errors.New("heatshrink: state machine in bad state on close")
	// ErrBadReader is returned when the io.Reader returns 0 bytes with no error
	ErrBadReader = errors.New("heatshrink: misbehaving io.Reader returned 0, nil")

	errNoBitsAvailable  = errors.New("no available bits")
	errOutputBufferFull = errors.New("output buffer full")
)
