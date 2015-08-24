package goheatshrink

import (
	"encoding/hex"
	"fmt"
	"log"
	"bytes"
	"io"
	"errors"
)

type Decoder interface {
	Decode(in []byte) ([]byte, error)
}

type decoder struct {
	inputSize   int
	inputIndex  int
	outputCount int
	outputIndex int
	headIndex   int
	state decodeState
	current     byte
	bitIndex    byte

	buffer       []byte
	window       uint8
	windowBuffer []byte
	lookahead    uint8
	bufferSize   int
}

type outputInfo struct {
	buf        []byte
	bufSize    int
	outputSize int
}

const NOBITS = ^uint16(0)

func NewDecoder(window uint8, lookahead uint8) Decoder {
	return &decoder{
		state:        decodeStateTagBit,
		window:       window,
		lookahead:    lookahead,
		windowBuffer: make([]byte, 1<<window),
	}
}

func (d *decoder) Decode(in []byte) ([]byte, error) {
	var out []byte
	ow := bytes.NewBuffer(out)
	chunkSize := 1<< d.window
	s := len(in)
	for {
		if s == 0 {
			d.finish()
			break
		}
		var s1 int
		if s > chunkSize {
			s1 = chunkSize
			s -= chunkSize
		} else {
			s1 = s
			s = 0
		}
		l("Chunking %v bytes...\n", len(in))
		d.decodeRead(in[:s1], ow)
		in = in[s1:]
	}
	return ow.Bytes(), nil
}

func (d *decoder) decodeRead(in []byte, out io.Writer) error {
	outBuf := make([]byte, 256)
	total := 0
	for total < len(in) {
		inputSize := d.sink(in)
		total += inputSize
		for {
			err, outputSize, outputBufferFull := d.poll(outBuf)
			out.Write(outBuf[:outputSize])
			l("Poll: %v %v %v %v\n", err, outputSize, outputBufferFull, total)
			if !outputBufferFull {
				break
			}
			l("Output buffer full, continue polling...")
		}
		if d.finish() {
			return nil
		}
		break
	}
	return errors.New("Ran out of input before finishing")
}



func (d *decoder) sink(in []byte) int {
	l("-- sinking %d bytes\n", len(in))
	l("buffer: %v", hex.EncodeToString(in))
	d.buffer = in
	d.inputSize += len(in)
	return d.inputSize
}

func logState(s decodeState) string {
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

func (d *decoder) poll(out []byte) (error, int, bool) {

	oi := &outputInfo{
		buf:        out,
		bufSize:    cap(out),
		outputSize: 0,
	}

	for {
		l("-- poll, state is %v, input_size %d\n", logState(d.state), d.inputSize)
		state := d.state
		switch state {
		case decodeStateTagBit:
			d.state = d.stateTagBit()
		case decodeStateYieldLiteral:
			d.state = d.stateYieldLiteral(oi)
		case decodeStateBackRefIndexMSB:
			d.state = d.stateBackRefIndexMSB()
		case decodeStateBackRefIndexLSB:
			d.state = d.stateBackRefIndexLSB()
		case decodeStateBackRefCountMSB:
			d.state = d.stateBackRefCountMSB()
		case decodeStateBackRefCountLSB:
			d.state = d.stateBackRefCountLSB()
		case decodeStateYieldBackRef:
			d.state = d.stateYieldBackRef(oi)
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if d.state == state {
			if oi.outputSize == cap(out) {
				return nil, oi.outputSize, true
			}
			return nil, oi.outputSize, false
		}
	}

}

func (d *decoder) stateTagBit() decodeState {
	bits := d.getBits(1)
	if bits == NOBITS {
		return decodeStateTagBit
	}
	if bits > 0 {
		return decodeStateYieldLiteral
	}
	if d.window > 8 {
		return decodeStateBackRefIndexMSB
	}
	d.outputIndex = 0
	return decodeStateBackRefIndexLSB
}

func (d *decoder) stateYieldLiteral(oi *outputInfo) decodeState {
	if oi.outputSize < oi.bufSize {
		bits := d.getBits(8)
		if bits == NOBITS {
			return decodeStateYieldLiteral
		}
		var mask uint16 = (1 << d.window) - 1
		var c byte = byte(bits & 0xFF)
		l("-- mask 0x%02x\n", mask)

		if isPrint(c) {
			l("-- emitting literal byte 0x%02x ('%c')\n", c, c)
		} else {
			l("-- emitting literal byte 0x%02x ('.')\n", c)
		}
		l("-- hsd->head_index 0x%02x\n", d.headIndex)
		d.windowBuffer[uint16(d.headIndex)&mask] = c
		d.headIndex++
		d.push(oi, c)
		return decodeStateTagBit
	}
	return decodeStateYieldLiteral
}

func (d *decoder) stateBackRefIndexMSB() decodeState {
	bitCount := d.window
	bits := d.getBits(bitCount - 8)
	l("-- backref index (msb), got 0x%04x (+1)\n", bits);
	if bits == NOBITS {
		return decodeStateBackRefIndexMSB
	}
	d.outputIndex = int(bits) << 8
	return decodeStateBackRefIndexLSB
}

func (d *decoder) stateBackRefIndexLSB() decodeState {
	bitCount := d.window
	var bits uint16
	if bitCount < 8 {
		bits = d.getBits(bitCount)
	} else {
		bits = d.getBits(8)
	}
	l("-- backref index (lsb), got 0x%04x (+1)\n", bits);
	if bits == NOBITS {
		return decodeStateBackRefIndexLSB
	}
	d.outputIndex |= int(bits)
	d.outputIndex++
	backRefBitCount := d.lookahead
	d.outputCount = 0
	if backRefBitCount > 8 {
		return decodeStateBackRefCountMSB
	}
	return decodeStateBackRefCountLSB
}

func (d *decoder) stateBackRefCountMSB() decodeState {
	backRefBitCount := d.lookahead
	bits := d.getBits(backRefBitCount - 8)
	l("-- backref count (msb), got 0x%04x (+1)\n", bits);
	if bits == NOBITS {
		return decodeStateBackRefCountMSB
	}
	d.outputCount = int(bits) << 8
	return decodeStateBackRefCountLSB
}

func (d *decoder) stateBackRefCountLSB() decodeState {
	backRefBitCount := d.lookahead
	var bits uint16
	if backRefBitCount < 8 {
		bits = d.getBits(backRefBitCount)
	} else {
		bits = d.getBits(8)
	}
	l("-- backref count (lsb), got 0x%04x (+1)\n", bits);
	if bits == NOBITS {
		return decodeStateBackRefCountLSB
	}
	d.outputCount |= int(bits)
	d.outputCount++
	return decodeStateYieldBackRef
}

func (d *decoder) stateYieldBackRef(oi *outputInfo) decodeState {
	count := oi.bufSize - oi.outputSize
	if count > 0 {
		if d.outputCount < count {
			count = d.outputCount
		}

		var mask int = (1 << d.window) - 1
		var negOffset int = d.outputIndex
		l("-- emitting %d bytes from -%d bytes back\n", count, negOffset)
		for i := 0; i < count; i++ {
			c := d.windowBuffer[(d.headIndex-negOffset)&mask]
			d.push(oi, c)
			d.windowBuffer[d.headIndex&mask] = c
			d.headIndex++
			l("  -- ++ 0x%02x\n", c)
		}
		d.outputCount -= count
		if d.outputCount == 0 {
			return decodeStateTagBit
		}
	}
	return decodeStateYieldBackRef
}

func (d *decoder) getBits(count uint8) uint16 {
	var accumulator uint16 = 0

	if count > 15 {
		return NOBITS
	}
	l("-- popping %d bit(s)\n", count)
	if d.inputSize == 0 {
		if d.bitIndex < (1 << (count - 1)) {
			return NOBITS
		}
	}
	var i uint8
	for i = 0; i < count; i++ {
		if d.bitIndex == 0x0 {
			if d.inputSize == 0 {
				l("out of bits, suspending w/ accumulator of %u (0x%02x)\n", accumulator, accumulator)
				return NOBITS
			}
			l("buffer: %v", hex.EncodeToString(d.buffer))
			l("buffer: %v", hex.EncodeToString(d.windowBuffer))
			d.current = d.buffer[d.inputIndex]
			l("  -- pulled byte 0x%02x at %d\n", d.current, d.inputIndex)
			d.inputIndex++
			if d.inputIndex == d.inputSize {
				d.inputIndex = 0
				d.inputSize = 0
			}
			d.bitIndex = 0x80
		}
		accumulator <<= 1
		if d.current&d.bitIndex > 0 {
			accumulator |= 0x01
			l("  -- got 1, accumulator 0x%04x, bit_index 0x%02x\n", accumulator, d.bitIndex)
		} else {
			l("  -- got 0, accumulator 0x%04x, bit_index 0x%02x\n", accumulator, d.bitIndex)
		}
		d.bitIndex >>= 1
	}
	if count > 1 {
		l("  -- accumulated %08x\n", accumulator)
	}
	return accumulator
}

func (d *decoder) finish() bool {
	switch d.state {
	case decodeStateTagBit:
		if d.inputSize == 0 {
			return true
		}
		return false
	case decodeStateBackRefIndexLSB:
	case decodeStateBackRefIndexMSB:
	case decodeStateBackRefCountLSB:
	case decodeStateBackRefCountMSB:
		if d.inputSize == 0 {
			return true
		}
		return false
	case decodeStateYieldLiteral:
		if d.inputSize == 0 {
			return true
		}
		return false

	}
	return false
}

func isPrint(b byte) bool {
	return b > 0x1f && b != 0x7f
}

func (d *decoder) push(oi *outputInfo, b byte) {
	if isPrint(b) {
		l(" -- pushing byte: 0x%02x ('%c')\n", b, b)
	} else {
		l(" -- pushing byte: 0x%02x ('.')\n", b)
	}
	oi.buf[oi.outputSize] = b
	oi.outputSize++
}

func l(format string, v ...interface{}) {
	//log.Printf(format, v...)
}
