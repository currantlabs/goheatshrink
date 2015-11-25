package goheatshrink

import (
	"io"
	"log"
)

// ReadResetter groups an io.Reader with a Reset method, which can switch to a new underlying io.Reader.
// This permits reusing a io.Reader instead of allocating a new one.
type ReadResetter interface {
	io.Reader
	// Reset discards any buffered data and resets the Resetter as if it was
	// newly initialized with the given reader.
	Reset(r io.Reader)
}

type reader struct {
	*config

	inner     io.Reader

	inputBuffer []byte
	inputSize   int
	inputIndex  int

	headIndex int
	state     decodeState
	current   byte
	bitIndex  byte

	buffer       []byte
	windowBuffer []byte
	bufferSize   int

	outputCount        int
	outputBackRefIndex int
}

type decodeState int

const (
	decodeStateTagBit decodeState = iota
	decodeStateYieldLiteral
	decodeStateBackRefIndexMSB
	decodeStateBackRefIndexLSB
	decodeStateBackRefCountMSB
	decodeStateBackRefCountLSB
	decodeStateYieldBackRef
)

// NewReaderConfig creates a new ReadResetter reading the given io.Reader.
//
// options modifies the default configuration values to use when decompressing
func NewReader(r io.Reader, options ...func(*config)) ReadResetter {
	hr := &reader{
		config: &config{window:defaultWindow, lookahead:defaultLookahead},
		inner:        r,
		state:        decodeStateTagBit,
	}
	for _, option := range options {
		option(hr.config)
	}
	hr.windowBuffer = make([]byte, 1<<hr.window)
	hr.inputBuffer = make([]byte, 1<<hr.window)
	return hr;
}

func (r *reader) Read(out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	var in []byte
	if r.inputSize > 0 {
		// Still have leftover bytes from last read
		in = r.inputBuffer[r.inputSize:]
	} else {
		in = r.inputBuffer
	}
	count, err := r.inner.Read(in)
	r.inputSize += count
	if r.inputSize > 0 {
		return r.decodeRead(r.inputBuffer[:r.inputSize], out)
	} else if err != nil {
		if err == io.EOF {
			if r.finish() {
				return 0, io.EOF
			}
			return 0, ErrTruncated
		}
		return 0, err
	}
	return 0, nil
}

// Reset clears the state of the Reader r such that it is equivalent to its initial state
func (r *reader) Reset(new io.Reader) {
	for i := range r.buffer {
		r.buffer[i] = 0
	}
	for i := range r.windowBuffer {
		r.windowBuffer[i] = 0
	}
	for i := range r.inputBuffer {
		r.inputBuffer[i] = 0
	}
	r.state = decodeStateTagBit
	r.inputIndex = 0
	r.inputSize = 0
	r.bitIndex = 0
	r.current = 0x0
	r.outputCount = 0
	r.outputBackRefIndex = 0
	r.headIndex = 0
	r.inner = new
}

type output struct {
	buf   []byte
	size  int
	index int
}

func (o *output) push(b byte) {
	o.buf[o.index] = b
	o.index++
}

func (r *reader) decodeRead(in []byte, out []byte) (int, error) {
	totalout := 0
	r.buffer = in
	r.inputSize = len(in)

	o := &output{
		buf:   out,
		size:  len(out),
		index: 0,
	}
	for {
		outputSize, err := r.poll(o)
		totalout += outputSize
		if err == errOutputBufferFull {
			return totalout, nil
		} else if err != nil {
			return totalout, err
		}
		if r.finish() {
			return totalout, nil
		}
	}
	return totalout, ErrTruncated
}

func (r *reader) poll(o *output) (int, error) {

	o.index = 0

	for {
		state := r.state
		switch state {
		case decodeStateTagBit:
			r.state = r.stateTagBit()
		case decodeStateYieldLiteral:
			r.state = r.stateYieldLiteral(o)
		case decodeStateBackRefIndexMSB:
			r.state = r.stateBackRefIndexMSB()
		case decodeStateBackRefIndexLSB:
			r.state = r.stateBackRefIndexLSB()
		case decodeStateBackRefCountMSB:
			r.state = r.stateBackRefCountMSB()
		case decodeStateBackRefCountLSB:
			r.state = r.stateBackRefCountLSB()
		case decodeStateYieldBackRef:
			r.state = r.stateYieldBackRef(o)
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if r.state == state {
			if o.index == cap(o.buf) {
				return o.index, errOutputBufferFull
			}
			return o.index, nil
		}
	}
}

func (r *reader) stateTagBit() decodeState {
	bits, err := r.getBits(1)
	if err == errNoBitsAvailable {
		return decodeStateTagBit
	}
	if bits > 0 {
		return decodeStateYieldLiteral
	}
	if r.window > 8 {
		return decodeStateBackRefIndexMSB
	}
	r.outputBackRefIndex = 0
	return decodeStateBackRefIndexLSB
}

func (r *reader) stateYieldLiteral(o *output) decodeState {
	if o.index < o.size {
		bits, err := r.getBits(8)
		if err == errNoBitsAvailable {
			return decodeStateYieldLiteral
		}
		var mask uint16 = (1 << r.window) - 1
		c := byte(bits & 0xFF)
		r.windowBuffer[uint16(r.headIndex)&mask] = c
		r.headIndex++
		o.push(c)
		return decodeStateTagBit
	}
	return decodeStateYieldLiteral
}

func (r *reader) stateBackRefIndexMSB() decodeState {
	bitCount := r.window
	bits, err := r.getBits(bitCount - 8)
	if err == errNoBitsAvailable {
		return decodeStateBackRefIndexMSB
	}
	r.outputBackRefIndex = int(bits) << 8
	return decodeStateBackRefIndexLSB
}

func (r *reader) stateBackRefIndexLSB() decodeState {
	bitCount := r.window
	var bits uint16
	var err error
	if bitCount < 8 {
		bits, err = r.getBits(bitCount)
	} else {
		bits, err = r.getBits(8)
	}
	if err == errNoBitsAvailable {
		return decodeStateBackRefIndexLSB
	}
	r.outputBackRefIndex |= int(bits)
	r.outputBackRefIndex++
	backRefBitCount := r.lookahead
	r.outputCount = 0
	if backRefBitCount > 8 {
		return decodeStateBackRefCountMSB
	}
	return decodeStateBackRefCountLSB
}

func (r *reader) stateBackRefCountMSB() decodeState {
	backRefBitCount := r.lookahead
	bits, err := r.getBits(backRefBitCount - 8)
	if err == errNoBitsAvailable {
		return decodeStateBackRefCountMSB
	}
	r.outputCount = int(bits) << 8
	return decodeStateBackRefCountLSB
}

func (r *reader) stateBackRefCountLSB() decodeState {
	backRefBitCount := r.lookahead
	var bits uint16
	var err error
	if backRefBitCount < 8 {
		bits, err = r.getBits(backRefBitCount)
	} else {
		bits, err = r.getBits(8)
	}
	if err == errNoBitsAvailable {
		return decodeStateBackRefCountLSB
	}
	r.outputCount |= int(bits)
	r.outputCount++
	return decodeStateYieldBackRef
}

func (r *reader) stateYieldBackRef(o *output) decodeState {
	count := o.size - o.index
	if count > 0 {
		if r.outputCount < count {
			count = r.outputCount
		}

		mask := (1 << r.window) - 1
		negOffset := r.outputBackRefIndex
		for i := 0; i < count; i++ {
			c := r.windowBuffer[(r.headIndex-negOffset)&mask]
			o.push(c)
			r.windowBuffer[r.headIndex&mask] = c
			r.headIndex++
		}
		r.outputCount -= count
		if r.outputCount == 0 {
			return decodeStateTagBit
		}
	}
	return decodeStateYieldBackRef
}

func (r *reader) getBits(count uint8) (uint16, error) {
	var accumulator uint16

	if count > 15 {
		return 0, errNoBitsAvailable
	}
	if r.inputSize == 0 {
		if r.bitIndex < (1 << (count - 1)) {
			return 0, errNoBitsAvailable
		}
	}
	var i uint8
	for i = 0; i < count; i++ {
		if r.bitIndex == 0x0 {
			if r.inputSize == 0 {
				return 0, errNoBitsAvailable
			}
			r.current = r.buffer[r.inputIndex]
			r.inputIndex++
			if r.inputIndex == r.inputSize {
				r.inputIndex = 0
				r.inputSize = 0
			}
			r.bitIndex = 0x80
		}
		accumulator <<= 1
		if r.current&r.bitIndex > 0 {
			accumulator |= 0x01
		}
		r.bitIndex >>= 1
	}
	return accumulator, nil
}

func (r *reader) finish() bool {
	switch r.state {
	case decodeStateTagBit, decodeStateBackRefIndexLSB, decodeStateBackRefIndexMSB, decodeStateBackRefCountLSB, decodeStateBackRefCountMSB, decodeStateYieldLiteral:
		if r.inputSize == 0 {
			return true
		}
		return false
	case decodeStateYieldBackRef:
		return false
	}
	return false
}
