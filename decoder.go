package goheatshrink

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
)

type Reader struct {
	reader io.Reader
	window    uint8
	lookahead uint8

	inputBuffer []byte
	inputSize   int
	inputIndex  int

	headIndex   int
	state       decodeState
	current     byte
	bitIndex    byte

	buffer       []byte
	windowBuffer []byte
	bufferSize   int

	outputBuffer []byte
	outputCount int
	outputSize int
	outputIndex int
	outputBackRefIndex int
}

const nobits = ^uint16(0)

var errOutputBufferFull = errors.New("output buffer full")

func NewReader(reader io.Reader, window uint8, lookahead uint8) *Reader {
	return &Reader{
		reader: reader,
		window:    window,
		lookahead: lookahead,
		state:        decodeStateTagBit,
		windowBuffer: make([]byte, 1<<window),
		inputBuffer: make([]byte, 1 << window),
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
	l("-- sinking %d bytes\n", len(in))
	l("buffer: %v", hex.EncodeToString(in))
	for {
		outputSize, err := r.poll()
		totalout += outputSize
		//out.Write(d.outbuf[:outputSize])
		l("Poll: %v %v %v %v\n", err, outputSize, len(in), r.inputSize)
		if err == errOutputBufferFull {
			l("Output buffer full, returning...")
			return totalout, errOutputBufferFull
		}
		if r.finish() {
			return totalout, nil
		}
	}
	return totalout, errors.New("Ran out of input before finishing")
}

func logDecodeState(s decodeState) string {
	switch s {
	case decodeStateTagBit:
		return fmt.Sprintf("%d (tag_bit)", s)
	case decodeStateYieldLiteral:
		return fmt.Sprintf("%d (yield_literal)", s)
	case decodeStateBackRefIndexMSB:
		return fmt.Sprintf("%d (backref_index_msb)", s)
	case decodeStateBackRefIndexLSB:
		return fmt.Sprintf("%d (backref_index_lsb)", s)
	case decodeStateBackRefCountMSB:
		return fmt.Sprintf("%d (backref_count_msb)", s)
	case decodeStateBackRefCountLSB:
		return fmt.Sprintf("%d (backref_count_lsb)", s)
	case decodeStateYieldBackRef:
		return fmt.Sprintf("%d (yield_backref)", s)
	default:
		log.Fatal("Unknown state: %v", s)
	}
	return fmt.Sprintf("%d", s)
}

func (r *Reader) poll() (int, error) {

	r.outputIndex = 0

	for {
		l("-- poll, state is %v, input_size %d\n", logDecodeState(r.state), r.inputSize)
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
	bits := r.getBits(1)
	if bits == nobits {
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
		bits := r.getBits(8)
		if bits == nobits {
			return decodeStateYieldLiteral
		}
		var mask uint16 = (1 << r.window) - 1
		c := byte(bits & 0xFF)
		l("-- mask 0x%02x\n", mask)

		if isPrint(c) {
			l("-- emitting literal byte 0x%02x ('%c')\n", c, c)
		} else {
			l("-- emitting literal byte 0x%02x ('.')\n", c)
		}
		l("-- hsd->head_index 0x%02x\n", r.headIndex)
		r.windowBuffer[uint16(r.headIndex)&mask] = c
		r.headIndex++
		r.push(c)
		return decodeStateTagBit
	}
	return decodeStateYieldLiteral
}

func (r *Reader) stateBackRefIndexMSB() decodeState {
	bitCount := r.window
	bits := r.getBits(bitCount-8)
	l("-- backref index (msb), got 0x%04x (+1)\n", bits)
	if bits == nobits {
		return decodeStateBackRefIndexMSB
	}
	r.outputBackRefIndex = int(bits) << 8
	return decodeStateBackRefIndexLSB
}

func (r *Reader) stateBackRefIndexLSB() decodeState {
	bitCount := r.window
	var bits uint16
	if bitCount < 8 {
		bits = r.getBits(bitCount)
	} else {
		bits = r.getBits(8)
	}
	l("-- backref index (lsb), got 0x%04x (+1)\n", bits)
	if bits == nobits {
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
	bits := r.getBits(backRefBitCount-8)
	l("-- backref count (msb), got 0x%04x (+1)\n", bits)
	if bits == nobits {
		return decodeStateBackRefCountMSB
	}
	r.outputCount = int(bits) << 8
	return decodeStateBackRefCountLSB
}

func (r *Reader) stateBackRefCountLSB() decodeState {
	backRefBitCount := r.lookahead
	var bits uint16
	if backRefBitCount < 8 {
		bits = r.getBits(backRefBitCount)
	} else {
		bits = r.getBits(8)
	}
	l("-- backref count (lsb), got 0x%04x (+1)\n", bits)
	if bits == nobits {
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
		l("-- emitting %d bytes from -%d bytes back\n", count, negOffset)
		for i := 0; i < count; i++ {
			c := r.windowBuffer[(r.headIndex-negOffset)&mask]
			r.push(c)
			r.windowBuffer[r.headIndex&mask] = c
			r.headIndex++
			l("  -- ++ 0x%02x\n", c)
		}
		r.outputCount -= count
		if r.outputCount == 0 {
			return decodeStateTagBit
		}
	}
	return decodeStateYieldBackRef
}

func (r *Reader) getBits(count uint8) uint16 {
	var accumulator uint16

	if count > 15 {
		return nobits
	}
	l("-- popping %d bit(s)\n", count)
	if r.inputSize == 0 {
		if r.bitIndex < (1 << (count - 1)) {
			return nobits
		}
	}
	var i uint8
	for i = 0; i < count; i++ {
		if r.bitIndex == 0x0 {
			if r.inputSize == 0 {
				l("out of bits, suspending w/ accumulator of %u (0x%02x)\n", accumulator, accumulator)
				return nobits
			}
			l("buffer: %v", hex.EncodeToString(r.buffer))
			l("buffer: %v", hex.EncodeToString(r.windowBuffer))
			r.current = r.buffer[r.inputIndex]
			l("  -- pulled byte 0x%02x at %d\n", r.current, r.inputIndex)
			r.inputIndex++
			if r.inputIndex == r.inputSize {
				r.inputIndex = 0
				r.inputSize = 0
			}
			r.bitIndex = 0x80
		}
		accumulator <<= 1
		if r.current& r.bitIndex > 0 {
			accumulator |= 0x01
			l("  -- got 1, accumulator 0x%04x, bit_index 0x%02x\n", accumulator, r.bitIndex)
		} else {
			l("  -- got 0, accumulator 0x%04x, bit_index 0x%02x\n", accumulator, r.bitIndex)
		}
		r.bitIndex >>= 1
	}
	if count > 1 {
		l("  -- accumulated %08x\n", accumulator)
	}
	return accumulator
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
	if isPrint(b) {
		l(" -- pushing byte: 0x%02x ('%c')\n", b, b)
	} else {
		l(" -- pushing byte: 0x%02x ('.')\n", b)
	}
	r.outputBuffer[r.outputIndex] = b
	r.outputIndex++
}

func l(format string, v ...interface{}) {
	//log.Printf(format, v...)
}
