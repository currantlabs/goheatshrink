package goheatshrink

import (
	"errors"
	"io"
	"log"
)

type Reader struct {
	reader    io.Reader
	window    uint8
	lookahead uint8

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

	outputBuffer       []byte
	outputCount        int
	outputSize         int
	outputIndex        int
	outputBackRefIndex int
}

var errNoBitsAvailable = errors.New("no available bits")
var errOutputBufferFull = errors.New("output buffer full")

func NewReader(reader io.Reader, window uint8, lookahead uint8) *Reader {
	return &Reader{
		reader:       reader,
		window:       window,
		lookahead:    lookahead,
		state:        decodeStateTagBit,
		windowBuffer: make([]byte, 1<<window),
		inputBuffer:  make([]byte, 1<<window),
	}
}

func (r *Reader) Read(out []byte) (int, error) {
	var in []byte
	if r.inputSize > 0 {
		// Still have leftover bytes from last read
		in = r.inputBuffer[r.inputSize:]
	} else {
		in = r.inputBuffer
	}
	incount, err := r.reader.Read(in)
	r.inputSize += incount
	if r.inputSize > 0 {
		return r.decodeRead(r.inputBuffer[:r.inputSize], out)
	} else if err == io.EOF && r.finish() {
		return 0, io.EOF
	}
	return 0, errors.New("Ran out of input before finishing")
}

func (r *Reader) decodeRead(in []byte, out []byte) (int, error) {
	totalout := 0
	r.buffer = in
	r.inputSize = len(in)
	r.outputBuffer = out
	r.outputSize = len(out)
	r.outputIndex = 0
	for {
		outputSize, err := r.poll()
		totalout += outputSize
		if err == errOutputBufferFull {
			return totalout, errOutputBufferFull
		}
		if r.finish() {
			return totalout, nil
		}
	}
	return totalout, errors.New("Ran out of input before finishing")
}

func (r *Reader) poll() (int, error) {

	r.outputIndex = 0

	for {
		state := r.state
		switch state {
		case decodeStateTagBit:
			r.state = r.stateTagBit()
		case decodeStateYieldLiteral:
			r.state = r.stateYieldLiteral()
		case decodeStateBackRefIndexMSB:
			r.state = r.stateBackRefIndexMSB()
		case decodeStateBackRefIndexLSB:
			r.state = r.stateBackRefIndexLSB()
		case decodeStateBackRefCountMSB:
			r.state = r.stateBackRefCountMSB()
		case decodeStateBackRefCountLSB:
			r.state = r.stateBackRefCountLSB()
		case decodeStateYieldBackRef:
			r.state = r.stateYieldBackRef()
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if r.state == state {
			if r.outputIndex == cap(r.outputBuffer) {
				return r.outputIndex, errOutputBufferFull
			}
			return r.outputIndex, nil
		}
	}
}

func (r *Reader) stateTagBit() decodeState {
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

func (r *Reader) stateYieldLiteral() decodeState {
	if r.outputIndex < r.outputSize {
		bits, err := r.getBits(8)
		if err == errNoBitsAvailable {
			return decodeStateYieldLiteral
		}
		var mask uint16 = (1 << r.window) - 1
		c := byte(bits & 0xFF)
		r.windowBuffer[uint16(r.headIndex)&mask] = c
		r.headIndex++
		r.push(c)
		return decodeStateTagBit
	}
	return decodeStateYieldLiteral
}

func (r *Reader) stateBackRefIndexMSB() decodeState {
	bitCount := r.window
	bits, err := r.getBits(bitCount - 8)
	if err == errNoBitsAvailable {
		return decodeStateBackRefIndexMSB
	}
	r.outputBackRefIndex = int(bits) << 8
	return decodeStateBackRefIndexLSB
}

func (r *Reader) stateBackRefIndexLSB() decodeState {
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

func (r *Reader) stateBackRefCountMSB() decodeState {
	backRefBitCount := r.lookahead
	bits, err := r.getBits(backRefBitCount - 8)
	if err == errNoBitsAvailable {
		return decodeStateBackRefCountMSB
	}
	r.outputCount = int(bits) << 8
	return decodeStateBackRefCountLSB
}

func (r *Reader) stateBackRefCountLSB() decodeState {
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

func (r *Reader) stateYieldBackRef() decodeState {
	count := r.outputSize - r.outputIndex
	if count > 0 {
		if r.outputCount < count {
			count = r.outputCount
		}

		mask := (1 << r.window) - 1
		negOffset := r.outputBackRefIndex
		for i := 0; i < count; i++ {
			c := r.windowBuffer[(r.headIndex-negOffset)&mask]
			r.push(c)
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

func (r *Reader) getBits(count uint8) (uint16, error) {
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

func (r *Reader) finish() bool {
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

func isPrint(b byte) bool {
	return b > 0x1f && b != 0x7f
}

func (r *Reader) push(b byte) {
	r.outputBuffer[r.outputIndex] = b
	r.outputIndex++
}

func l(format string, v ...interface{}) {
	//log.Printf(format, v...)
}
