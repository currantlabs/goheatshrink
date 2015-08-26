package goheatshrink

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"log"

	"github.com/go-errors/errors"
)

type Encoder interface {
	Encode([]byte) ([]byte, error)
}

type encoder struct {
	inputSize         uint16
	matchScanIndex    uint16
	matchLength       uint16
	matchPosition     uint16
	outgoingBits      uint16
	outgoingBitsCount uint8
	flags             encodeFlags
	state             encodeState
	current           byte
	bitIndex          uint8

	buffer    []byte
	window    uint8
	lookahead uint8
	index     []int16
}

const heatshrinkLiteralMarker byte = 0x01
const heatshrinkBackrefMarker byte = 0x00

/*
#if HEATSHRINK_DYNAMIC_ALLOC
#define HEATSHRINK_ENCODER_WINDOW_BITS(HSE) \
    ((HSE)->window_sz2)
#define HEATSHRINK_ENCODER_LOOKAHEAD_BITS(HSE) \
    ((HSE)->lookahead_sz2)
#define HEATSHRINK_ENCODER_INDEX(HSE) \
    ((HSE)->search_index)
struct hs_index {
    uint16_t size;
    int16_t index[];
};
#else
#define HEATSHRINK_ENCODER_WINDOW_BITS(_) \
    (HEATSHRINK_STATIC_WINDOW_BITS)
#define HEATSHRINK_ENCODER_LOOKAHEAD_BITS(_) \
    (HEATSHRINK_STATIC_LOOKAHEAD_BITS)
#define HEATSHRINK_ENCODER_INDEX(HSE) \
    (&(HSE)->search_index)
struct hs_index {
    uint16_t size;
    int16_t index[2 << HEATSHRINK_STATIC_WINDOW_BITS];
};
#endif
*/

func NewEncoder(window uint8, lookahead uint8) Encoder {
	bufSize := 2 << window
	return &encoder{
		state: encodeStateNotFull,
		bitIndex: 0x80,

		window:    window,
		lookahead: lookahead,
		buffer:    make([]byte, bufSize),
		index:     make([]int16, bufSize),
	}
}

func (e *encoder) Encode(in []byte) ([]byte, error) {
	var out []byte
	ow := bytes.NewBuffer(out)
	chunkSize := 1 << e.window
	s := len(in)
	for {
		var s1 int
		if s > chunkSize {
			s1 = chunkSize
			s -= chunkSize
		} else {
			s1 = s
			s = 0
		}
		//l("Chunking %v bytes...\n", len(in))
		done, err := e.encodeRead(in[:s1], ow)
		if err != nil {
			return nil, err
		}
		if done {
			return ow.Bytes(), nil
		}
		in = in[s1:]
	}
	return nil, errors.New("Ran out of input before finishing")
}

func (e *encoder) encodeRead(in []byte, out io.Writer) (bool, error) {
	outBuf := make([]byte, 256)
	var done uint16
	total := uint16(len(in))
	for {
		if len(in) > 0 {
			inputSize, err := e.sink(in)
			if err != nil {
				return false, err
			}
			done += inputSize
			in = in[inputSize:]
		}
		var outputSize int
		for {
			var res encodePollResult
			var err error
			err, outputSize, res = e.poll(outBuf)
			if err != nil {
				return false, err
			}
			out.Write(outBuf[:outputSize])
			//l("Poll: %v %v %v %v\n", err, outputSize, res, done)
			if res != encodePollResultMore {
				break
			}
			l("Output buffer full, continue polling...")
		}
		if outputSize == 0 && total == 0 {
			if e.finish() {
				return true, nil
			}
		}
		if done >= total {
			break
		}
	}
	return false, nil
}

func (e *encoder) sink(in []byte) (uint16, error) {
	if e.isFinishing() {
		return 0, errors.New("sinking while finishing")
	}
	if e.state != encodeStateNotFull {
		return 0, errors.New("sinking while processing")
	}
	//l("-- sinking %d bytes\n", len(in))
	offset := e.getInputBufferSize() + e.inputSize
	ibs := e.getInputBufferSize()
	remaining := ibs - e.inputSize
	var copySize uint16
	if remaining < uint16(len(in)) {
		copySize = remaining
	} else {
		copySize = uint16(len(in))
	}
	copy(e.buffer[offset:], in[:copySize])
	e.inputSize += copySize
	l("-- sunk %d bytes (of %d) into encoder at %d, input buffer now has %d\n",
		copySize, len(in), offset, e.inputSize)
	l("buffer: %v", hex.EncodeToString(e.buffer[:e.inputSize]))
	l("buffer: %v", hex.EncodeToString(e.buffer[e.getInputBufferSize():]))
	if copySize == remaining {
		l("-- internal buffer is now full\n")
		e.state = encodeStateFilled
	}
	return copySize, nil
}

func logEncodeState(s encodeState) string {
	switch s {
	case encodeStateNotFull:
		return fmt.Sprintf("%d (not_full)", s)
	case encodeStateFilled:
		return fmt.Sprintf("%d (filled)", s)
	case encodeStateSearch:
		return fmt.Sprintf("%d (search)", s)
	case encodeStateYieldTagBit:
		return fmt.Sprintf("%d (yield_tag_bit)", s)
	case encodeStateYieldLiteral:
		return fmt.Sprintf("%d (yield_literal)", s)
	case encodeStateYieldBackRefIndex:
		return fmt.Sprintf("%d (yield_br_index)", s)
	case encodeStateYieldBackRefLength:
		return fmt.Sprintf("%d (yield_br_length)", s)
	case encodeStateSaveBacklog:
		return fmt.Sprintf("%d (save_backlog)", s)
	case encodeStateFlushBits:
		return fmt.Sprintf("%d (flush_bits)", s)
	case encodeStateDone:
		return fmt.Sprintf("%d (done)", s)
	default:
		log.Fatal("Unknown state: %v", s)
	}
	return fmt.Sprintf("%d", s)
}

type encodePollResult int

const (
	encodePollResultEmpty encodePollResult = iota
	encodePollResultMore
	encodePollResultErrorNull
	encodePollResultErrorMisuse
)

func (e *encoder) poll(out []byte) (error, int, encodePollResult) {

	oi := &outputInfo{
		buf:        out,
		bufSize:    cap(out),
		outputSize: 0,
	}

	for {
		l("-- polling, state %v, flags 0x%02x\n", logEncodeState(e.state), e.flags)
		state := e.state
		switch state {
		case encodeStateNotFull:
			return nil, oi.outputSize, encodePollResultEmpty
		case encodeStateFilled:
			e.doIndexing()
			e.state = encodeStateSearch
		case encodeStateSearch:
			e.state = e.stateStepSearch()
		case encodeStateYieldTagBit:
			e.state = e.stateYieldTagBit(oi)
		case encodeStateYieldLiteral:
			e.state = e.stateYieldLiteral(oi)
		case encodeStateYieldBackRefIndex:
			e.state = e.stateYieldBackRefIndex(oi)
		case encodeStateYieldBackRefLength:
			e.state = e.stateYieldBackRefLength(oi)
		case encodeStateSaveBacklog:
			e.state = e.stateSaveBacklog()
		case encodeStateFlushBits:
			e.state = e.stateFlushBitBuffer(oi)
		case encodeStateDone:
			return nil, oi.outputSize, encodePollResultEmpty
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if e.state == state {
			if oi.outputSize == cap(out) {
				return nil, oi.outputSize, encodePollResultMore
			}
		}
	}

}

func (e *encoder) finish() bool {
	l("-- setting is_finishing flag\n")
	e.flags |= encodeFlagsFinishing
	if e.state == encodeStateNotFull {
		e.state = encodeStateFilled
	}
	return e.state == encodeStateDone
}

func (e *encoder) stateStepSearch() encodeState {
	var windowLength uint16 = 1 << e.window
	var lookaheadLength uint16 = 1 << e.lookahead
	msi := e.matchScanIndex
	l("## step_search, scan @ +%d (%d/%d), input size %d\n",
		msi, e.inputSize+msi, 2*windowLength, e.inputSize)
	fin := e.isFinishing()
	var lookaheadCompare uint16
	if fin {
		lookaheadCompare = 1
	} else {
		lookaheadCompare = lookaheadLength
	}
	if msi > e.inputSize-lookaheadCompare {
		l("-- end of search @ %d\n", msi);
		if fin {
			return encodeStateFlushBits
		}
		return encodeStateSaveBacklog
	}
	ibs := e.getInputBufferSize()
	end := ibs + msi
	start := end - windowLength
	maxPossible := lookaheadLength
	if e.inputSize-msi < lookaheadLength {
		maxPossible = e.inputSize - msi
	}
	matchPos, matchLength := e.findLongestMatch(start, end, maxPossible)
	if matchPos == matchNotFound {
		l("ss Match not found\n")
		e.matchScanIndex++
		e.matchLength = 0
		return encodeStateYieldTagBit
	}
	l("ss Found match of %d bytes at %d\n", matchLength, matchPos)
	e.matchPosition = matchPos
	e.matchLength = matchLength
	l("ss 1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse) %d\n", 1<<e.window)
	l("ss match_pos %d\n", matchPos)
	if matchPos < (1<<e.window) {
		l("ss match_pos < (1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse)) 1\n")
	} else {
		l("ss match_pos < (1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse)) 0\n")
	}

	return encodeStateYieldTagBit
}

func (e *encoder) stateYieldTagBit(oi *outputInfo) encodeState {
	if e.canTakeByte(oi) {
		if e.matchLength == 0 {
			e.addTagBit(oi, heatshrinkLiteralMarker)
			return encodeStateYieldLiteral
		}
		e.addTagBit(oi, heatshrinkBackrefMarker)
		e.outgoingBits = e.matchPosition - 1
		e.outgoingBitsCount = e.window
		return encodeStateYieldBackRefIndex
	}
	return encodeStateYieldTagBit
}

func (e *encoder) stateYieldLiteral(oi *outputInfo) encodeState {
	if e.canTakeByte(oi) {
		e.pushLiteralByte(oi)
		return encodeStateSearch
	}
	return encodeStateYieldLiteral
}

func (e *encoder) stateYieldBackRefIndex(oi *outputInfo) encodeState {
	if e.canTakeByte(oi) {
		l("-- yielding backref index %d\n", e.matchPosition)
		if e.pushOutgoingBits(oi) > 0 {
			return encodeStateYieldBackRefIndex
		}
		e.outgoingBits = e.matchLength - 1
		e.outgoingBitsCount = e.lookahead
		return encodeStateYieldBackRefLength
	}
	return encodeStateYieldBackRefIndex
}

func (e *encoder) stateYieldBackRefLength(oi *outputInfo) encodeState {
	if e.canTakeByte(oi) {
		l("-- yielding backref length %d\n", e.matchLength)
		if e.pushOutgoingBits(oi) > 0 {
			return encodeStateYieldBackRefLength
		}
		e.matchScanIndex += e.matchLength
		e.matchLength = 0
		return encodeStateSearch
	}
	return encodeStateYieldBackRefLength
}

func (e *encoder) stateSaveBacklog() encodeState {
	l("-- saving backlog\n")
	e.saveBacklog()
	return encodeStateNotFull
}

func (e *encoder) stateFlushBitBuffer(oi *outputInfo) encodeState {
	if e.bitIndex == 0x80 {
		l("-- done!\n")
		return encodeStateDone
	}
	if e.canTakeByte(oi) {
		l("-- flushing remaining byte (bit_index == 0x%02x)\n", e.bitIndex)
		oi.buf[oi.outputSize] = e.current
		oi.outputSize++
		l("-- done!\n")
		return encodeStateDone
	}
	return encodeStateFlushBits
}

const matchNotFound = ^uint16(0)

func (e *encoder) findLongestMatch(start uint16, end uint16, max uint16) (uint16, uint16) {
	l("-- scanning for match of buf[%d:%d] between buf[%d:%d] (max %d bytes)\n",
		end, end+max, start, end+max-1, max)
	var matchMaxLength uint16
	var matchIndex = matchNotFound
	var len uint16
	needlepoint := e.buffer[end:]
	pos := e.index[end]
	l("pos: %d\n", pos)

	for pos-int16(start) >= 0 {
		pospoint := e.buffer[pos:]
		len = 0
		l("  --> cmp buf[%d] == 0x%02x against %02x (start %d)\n", pos+int16(len), pospoint[len], needlepoint[len], start)
		if pospoint[matchMaxLength] != needlepoint[matchMaxLength] {
			pos = e.index[pos]
			continue
		}
		for len = 1; len < max; len++ {
			if pospoint[len] != needlepoint[len] {
				break
			}
		}
		if len > matchMaxLength {
			matchMaxLength = len
			matchIndex = uint16(pos)
			if len == max {
				break
			}
		}
		pos = e.index[pos]
	}
	breakEven := 1 + e.window + e.lookahead
	if 8 * uint(matchMaxLength) > uint(breakEven) {
		l("-- best match: %d bytes at -%d\n", matchMaxLength, end-matchIndex)
		return end - matchIndex, matchMaxLength
	}
	l("-- none found\n")
	return matchNotFound, 0
}

func (e *encoder) pushLiteralByte(oi *outputInfo) {
	processedOffset := e.matchScanIndex - 1
	inputOffset := e.getInputBufferSize() + processedOffset

	b := e.buffer[inputOffset]
	if isPrint(b) {
		l("-- yielded literal byte 0x%02x ('%c') from +%d\n", b, b, inputOffset)
	} else {
		l("-- yielded literal byte 0x%02x ('.') from +%d\n", b, inputOffset)
	}
	e.pushBits(8, b, oi)
}

func (e *encoder) pushBits(count uint8, bits byte, oi *outputInfo) {
	l("++ push_bits: %d bits, input of 0x%02x\n", count, bits)
	if count == 8 && e.bitIndex == 0x80 {
		oi.buf[oi.outputSize] = bits
		oi.outputSize++
	} else {
		var i int16
		for i = int16(count) - 1; i >= 0; i-- {
			bit := bits & (1 << uint16(i))
			if bit > 0 {
				e.current |= e.bitIndex
				l("  -- setting bit %d at bit index 0x%02x, byte => 0x%02x\n", 1, e.bitIndex, e.current);
			} else {
				l("  -- setting bit %d at bit index 0x%02x, byte => 0x%02x\n", 0, e.bitIndex, e.current);
			}
			e.bitIndex >>= 1
			if e.bitIndex == 0 {
				e.bitIndex = 0x80
				l(" > pushing byte 0x%02x\n", e.current)
				oi.buf[oi.outputSize] = e.current
				oi.outputSize++
				e.current = 0x0
			}
		}
	}
}

func (e *encoder) pushOutgoingBits(oi *outputInfo) uint8 {
	var count uint8
	var bits byte
	if e.outgoingBitsCount > 8 {
		count = 8
		bits = byte(e.outgoingBits >> (e.outgoingBitsCount - 8))
	} else {
		count = e.outgoingBitsCount
		bits = byte(e.outgoingBits)
	}
	if count > 0 {
		l("-- pushing %d outgoing bits: 0x%02x\n", count, bits)
		e.pushBits(count, bits, oi)
		e.outgoingBitsCount -= count
	}
	return count
}

func (e *encoder) addTagBit(oi *outputInfo, tag byte) {
	l("-- adding tag bit: %d\n", tag)
	e.pushBits(1, tag, oi)
}

func (e *encoder) saveBacklog() {
	msi := e.matchScanIndex
	copy(e.buffer, e.buffer[msi:])
	e.matchScanIndex = 0
	e.inputSize -= msi
}

func (e *encoder) getInputBufferSize() uint16 {
	return 1 << e.window
}

func (e *encoder) doIndexing() {
	var last [256]int16
	for i := range last {
		last[i] = -1
	}
	ibs := e.getInputBufferSize()
	end := ibs + e.inputSize
	var i uint16
	for i = 0; i < end; i++ {
		v := e.buffer[i]
		lv := last[v]
		l("-- setting index: %d, v: %d lv: %d\n", i, v, lv)
		e.index[i] = lv
		last[v] = int16(i)
 	}
}

func (e *encoder) isFinishing() bool {
	return e.flags&encodeFlagsFinishing == encodeFlagsFinishing
}

func (e *encoder) canTakeByte(oi *outputInfo) bool {
	return oi.outputSize < oi.bufSize
}
