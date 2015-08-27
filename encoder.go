package goheatshrink

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"

	"github.com/go-errors/errors"
	"bufio"
)

type writer interface {
	io.ByteWriter
	Flush() error
}

type Encoder struct {
	inputSize         int
	matchScanIndex    int
	matchLength       int
	matchPosition     int
	outgoingBits      int
	outgoingBitsCount uint8
	flags             encodeFlags
	state             encodeState
	current           byte
	bitIndex          uint8

	buffer    []byte
	window    uint8
	lookahead uint8
	index     []int16

	w writer
	outputTotal int
}

const heatshrinkLiteralMarker byte = 0x01
const heatshrinkBackrefMarker byte = 0x00

func NewWriter(w io.Writer, window uint8, lookahead uint8) *Encoder {
	bufSize := 2 << window
	bw, ok := w.(writer)
	if !ok {
		bw = bufio.NewWriter(w)
	}
	return &Encoder{
		w: bw,
		state:    encodeStateNotFull,
		bitIndex: 0x80,

		window:    window,
		lookahead: lookahead,
		buffer:    make([]byte, bufSize),
		index:     make([]int16, bufSize),
	}
}

func (e *Encoder) Write(p []byte) (n int, err error) {
	l("Writing %v bytes\n", len(p))
	var done int
	total := len(p)
	for {
		if len(p) > 0 {
			inputSize, err := e.sink(p)
			done += int(inputSize)
			if err != nil {
				return done, err
			}
			p = p[inputSize:]
		}

		outputSize, err, notFull := e.poll()
		l("Poll: %v %v %v %v/%v\n", outputSize, err, notFull, done, total)
		if err != nil {
			return done, err
		}


		if outputSize == 0 {
			if e.finish() {
				return done, nil
			}
		}
		if notFull {
			continue
		}
		if done >= total {
			break
		}
	}
	return done, nil
}

func (e *Encoder) Close() error {
	if e.finish() {
		return nil
	}
	_, err, _ := e.poll()
	if err != nil {
		return err
	}
	if e.finish() {
		return nil
	}
	return errors.New("unable to finish")
}


func (e *Encoder) sink(in []byte) (int, error) {
	if e.isFinishing() {
		return 0, errors.New("sinking while finishing")
	}
	if e.state != encodeStateNotFull {
		return 0, errors.New("sinking while processing")
	}
	l("-- sinking %d bytes; inputSize %v\n", len(in), e.inputSize)
	offset := e.getInputBufferSize() + e.inputSize
	ibs := e.getInputBufferSize()
	remaining := ibs - e.inputSize
	l("-- remaining %v\n", remaining)
	var copySize int
	if remaining < len(in) {
		l("-- smaller %v\n", len(in))
		copySize = remaining
	} else {
		l("-- taking it all copySize %v\n", uint16(len(in)))
		copySize = len(in)
	}
	l("-- copySize %v\n", copySize)
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

func (e *Encoder) poll() (int, error, bool) {

	e.outputTotal = 0
	var err error

	for {
		l("-- polling, state %v, flags 0x%02x\n", logEncodeState(e.state), e.flags)
		state := e.state
		switch state {
		case encodeStateNotFull:
			return e.outputTotal, nil, true
		case encodeStateFilled:
			e.doIndexing()
			e.state = encodeStateSearch
		case encodeStateSearch:
			e.state = e.stateStepSearch()
		case encodeStateYieldTagBit:
			e.state, err = e.stateYieldTagBit()
		case encodeStateYieldLiteral:
			e.state, err = e.stateYieldLiteral()
		case encodeStateYieldBackRefIndex:
			e.state, err = e.stateYieldBackRefIndex()
		case encodeStateYieldBackRefLength:
			e.state, err = e.stateYieldBackRefLength()
		case encodeStateSaveBacklog:
			e.state = e.stateSaveBacklog()
		case encodeStateFlushBits:
			e.state, err = e.stateFlushBitBuffer()
		case encodeStateDone:
			return e.outputTotal, nil, false
		case encodeStateInvalid:
			log.Fatal("Invalid state: %v", state)
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if err != nil {
			return e.outputTotal, err, false
		}

	}

}

func (e *Encoder) finish() bool {
	l("-- setting is_finishing flag\n")
	e.flags |= encodeFlagsFinishing
	if e.state == encodeStateNotFull {
		e.state = encodeStateFilled
	}
	return e.state == encodeStateDone
}

func (e *Encoder) stateStepSearch() encodeState {
	var windowLength int = 1 << e.window
	var lookaheadLength int = 1 << e.lookahead
	msi := e.matchScanIndex
	l("## step_search, scan @ +%d (%d/%d), input size %d\n",
		msi, e.inputSize+msi, 2*windowLength, e.inputSize)
	fin := e.isFinishing()
	var lookaheadCompare int
	if fin {
		lookaheadCompare = 1
	} else {
		lookaheadCompare = lookaheadLength
	}
	if msi > e.inputSize-lookaheadCompare {
		l("-- end of search @ %d\n", msi)
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
	if matchPos < (1 << e.window) {
		l("ss match_pos < (1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse)) 1\n")
	} else {
		l("ss match_pos < (1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse)) 0\n")
	}

	return encodeStateYieldTagBit
}

func (e *Encoder) stateYieldTagBit() (encodeState, error) {
	if e.matchLength == 0 {
		err := e.addTagBit(heatshrinkLiteralMarker)
		if err != nil {
			return encodeStateInvalid, err
		}
		return encodeStateYieldLiteral, nil
	}
	err := e.addTagBit(heatshrinkBackrefMarker)
	if err != nil {
		return encodeStateInvalid, err
	}
	e.outgoingBits = e.matchPosition - 1
	e.outgoingBitsCount = e.window
	return encodeStateYieldBackRefIndex, nil
}

func (e *Encoder) stateYieldLiteral() (encodeState, error) {
	err := e.pushLiteralByte()
	if err != nil {
		return encodeStateInvalid, err
	}
	return encodeStateSearch, nil
}

func (e *Encoder) stateYieldBackRefIndex() (encodeState, error) {
	l("-- yielding backref index %d\n", e.matchPosition)
	count, err := e.pushOutgoingBits()
	if err != nil {
		return encodeStateInvalid, err
	}
	if count > 0 {
		return encodeStateYieldBackRefIndex, nil
	}
	e.outgoingBits = e.matchLength - 1
	e.outgoingBitsCount = e.lookahead
	return encodeStateYieldBackRefLength, nil
}

func (e *Encoder) stateYieldBackRefLength() (encodeState, error) {
	l("-- yielding backref length %d\n", e.matchLength)
	count, err := e.pushOutgoingBits()
	if err != nil {
		return encodeStateInvalid, err
	}
	if count > 0 {
		return encodeStateYieldBackRefLength, nil
	}
	e.matchScanIndex += e.matchLength
	e.matchLength = 0
	return encodeStateSearch, nil
}

func (e *Encoder) stateSaveBacklog() encodeState {
	l("-- saving backlog\n")
	e.saveBacklog()
	return encodeStateNotFull
}

func (e *Encoder) stateFlushBitBuffer() (encodeState, error) {
	if e.bitIndex == 0x80 {
		l("-- done!\n")
		return encodeStateDone, nil
	}
	l("-- flushing remaining byte (bit_index == 0x%02x)\n", e.bitIndex)
	err := e.w.WriteByte(e.current)
	if err != nil {
		l("-- error flushing: %v\n", err)
		return encodeStateInvalid, err
	}
	e.outputTotal++
	l("-- done!\n")
	return encodeStateDone, nil
}

const matchNotFound = ^int(0)

func (e *Encoder) findLongestMatch(start int, end int, max int) (int, int) {
	l("-- scanning for match of buf[%d:%d] between buf[%d:%d] (max %d bytes)\n",
		end, end+max, start, end+max-1, max)
	var matchMaxLength int
	var matchIndex = matchNotFound
	var len int
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
			matchIndex = int(pos)
			if len == max {
				break
			}
		}
		pos = e.index[pos]
	}
	breakEven := 1 + e.window + e.lookahead
	if 8*uint(matchMaxLength) > uint(breakEven) {
		l("-- best match: %d bytes at -%d\n", matchMaxLength, end-matchIndex)
		return end - matchIndex, matchMaxLength
	}
	l("-- none found\n")
	return matchNotFound, 0
}

func (e *Encoder) pushLiteralByte() error {
	processedOffset := e.matchScanIndex - 1
	inputOffset := e.getInputBufferSize() + processedOffset

	b := e.buffer[inputOffset]
	if isPrint(b) {
		l("-- yielded literal byte 0x%02x ('%c') from +%d\n", b, b, inputOffset)
	} else {
		l("-- yielded literal byte 0x%02x ('.') from +%d\n", b, inputOffset)
	}
	return e.pushBits(8, b)
}

func (e *Encoder) pushBits(count uint8, bits byte) error {
	l("++ push_bits: %d bits, input of 0x%02x\n", count, bits)
	if count == 8 && e.bitIndex == 0x80 {
		err := e.w.WriteByte(bits)
		if err == nil {
			e.outputTotal++
		}
		return err
	}
	var i int16
	var err error
	for i = int16(count) - 1; i >= 0; i-- {
		bit := bits & (1 << uint16(i))
		if bit > 0 {
			e.current |= e.bitIndex
			l("  -- setting bit %d at bit index 0x%02x, byte => 0x%02x\n", 1, e.bitIndex, e.current)
		} else {
			l("  -- setting bit %d at bit index 0x%02x, byte => 0x%02x\n", 0, e.bitIndex, e.current)
		}
		e.bitIndex >>= 1
		if e.bitIndex == 0 {
			e.bitIndex = 0x80
			l(" > pushing byte 0x%02x\n", e.current)
			err = e.w.WriteByte(e.current)
			if err != nil {
				return err
			}
			e.outputTotal++
			e.current = 0x0
		}
	}
	return nil
}

func (e *Encoder) pushOutgoingBits() (uint8, error) {
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
		err := e.pushBits(count, bits)
		if err != nil {
			return 0, err
		}
		e.outgoingBitsCount -= count
	}
	return count, nil
}

func (e *Encoder) addTagBit(tag byte) error {
	l("-- adding tag bit: %d\n", tag)
	return e.pushBits(1, tag)
}

func (e *Encoder) saveBacklog() {
	msi := e.matchScanIndex
	copy(e.buffer, e.buffer[msi:])
	e.matchScanIndex = 0
	e.inputSize -= msi
}

func (e *Encoder) getInputBufferSize() int {
	return 1 << e.window
}

func (e *Encoder) doIndexing() {
	var last [256]int16
	for i := range last {
		last[i] = -1
	}
	ibs := e.getInputBufferSize()
	end := ibs + e.inputSize
	var i int
	for i = 0; i < end; i++ {
		v := e.buffer[i]
		lv := last[v]
		l("-- setting index: %d, v: %d lv: %d\n", i, v, lv)
		e.index[i] = lv
		last[v] = int16(i)
	}
}

func (e *Encoder) isFinishing() bool {
	return e.flags&encodeFlagsFinishing == encodeFlagsFinishing
}

