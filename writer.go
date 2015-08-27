package goheatshrink

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
)

type writer interface {
	io.ByteWriter
	Flush() error
}

type Writer struct {
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

	w           writer
	outputTotal int
}

const heatshrinkLiteralMarker byte = 0x01
const heatshrinkBackrefMarker byte = 0x00

func NewWriter(w io.Writer, window uint8, lookahead uint8) *Writer {
	bufSize := 2 << window
	bw, ok := w.(writer)
	if !ok {
		bw = bufio.NewWriter(w)
	}
	return &Writer{
		w:        bw,
		state:    encodeStateNotFull,
		bitIndex: 0x80,

		window:    window,
		lookahead: lookahead,
		buffer:    make([]byte, bufSize),
		index:     make([]int16, bufSize),
	}
}

func (w *Writer) Write(p []byte) (n int, err error) {
	l("Writing %v bytes\n", len(p))
	var done int
	total := len(p)
	for {
		if len(p) > 0 {
			inputSize, err := w.sink(p)
			done += int(inputSize)
			if err != nil {
				return done, err
			}
			p = p[inputSize:]
		}

		outputSize, err, notFull := w.poll()
		l("Poll: %v %v %v %v/%v\n", outputSize, err, notFull, done, total)
		if err != nil {
			return done, err
		}

		if outputSize == 0 {
			if w.finish() {
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

func (w *Writer) Close() error {
	if w.finish() {
		return nil
	}
	_, err, _ := w.poll()
	if err != nil {
		return err
	}
	if w.finish() {
		return nil
	}
	return errors.New("unable to finish")
}

func (w *Writer) sink(in []byte) (int, error) {
	if w.isFinishing() {
		return 0, errors.New("sinking while finishing")
	}
	if w.state != encodeStateNotFull {
		return 0, errors.New("sinking while processing")
	}
	l("-- sinking %d bytes; inputSize %v\n", len(in), w.inputSize)
	offset := w.getInputBufferSize() + w.inputSize
	ibs := w.getInputBufferSize()
	remaining := ibs - w.inputSize
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
	copy(w.buffer[offset:], in[:copySize])
	w.inputSize += copySize
	l("-- sunk %d bytes (of %d) into encoder at %d, input buffer now has %d\n",
		copySize, len(in), offset, w.inputSize)
	l("buffer: %v", hex.EncodeToString(w.buffer[:w.inputSize]))
	l("buffer: %v", hex.EncodeToString(w.buffer[w.getInputBufferSize():]))
	if copySize == remaining {
		l("-- internal buffer is now full\n")
		w.state = encodeStateFilled
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

func (w *Writer) poll() (int, error, bool) {

	w.outputTotal = 0
	var err error

	for {
		l("-- polling, state %v, flags 0x%02x\n", logEncodeState(w.state), w.flags)
		state := w.state
		switch state {
		case encodeStateNotFull:
			return w.outputTotal, nil, true
		case encodeStateFilled:
			w.doIndexing()
			w.state = encodeStateSearch
		case encodeStateSearch:
			w.state = w.stateStepSearch()
		case encodeStateYieldTagBit:
			w.state, err = w.stateYieldTagBit()
		case encodeStateYieldLiteral:
			w.state, err = w.stateYieldLiteral()
		case encodeStateYieldBackRefIndex:
			w.state, err = w.stateYieldBackRefIndex()
		case encodeStateYieldBackRefLength:
			w.state, err = w.stateYieldBackRefLength()
		case encodeStateSaveBacklog:
			w.state = w.stateSaveBacklog()
		case encodeStateFlushBits:
			w.state, err = w.stateFlushBitBuffer()
		case encodeStateDone:
			return w.outputTotal, nil, false
		case encodeStateInvalid:
			log.Fatal("Invalid state: %v", state)
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if err != nil {
			return w.outputTotal, err, false
		}

	}

}

func (w *Writer) finish() bool {
	l("-- setting is_finishing flag\n")
	w.flags |= encodeFlagsFinishing
	if w.state == encodeStateNotFull {
		w.state = encodeStateFilled
	}
	return w.state == encodeStateDone
}

func (w *Writer) stateStepSearch() encodeState {
	var windowLength int = 1 << w.window
	var lookaheadLength int = 1 << w.lookahead
	msi := w.matchScanIndex
	l("## step_search, scan @ +%d (%d/%d), input size %d\n",
		msi, w.inputSize+msi, 2*windowLength, w.inputSize)
	fin := w.isFinishing()
	var lookaheadCompare int
	if fin {
		lookaheadCompare = 1
	} else {
		lookaheadCompare = lookaheadLength
	}
	if msi > w.inputSize-lookaheadCompare {
		l("-- end of search @ %d\n", msi)
		if fin {
			return encodeStateFlushBits
		}
		return encodeStateSaveBacklog
	}
	ibs := w.getInputBufferSize()
	end := ibs + msi
	start := end - windowLength
	maxPossible := lookaheadLength
	if w.inputSize-msi < lookaheadLength {
		maxPossible = w.inputSize - msi
	}
	matchPos, matchLength := w.findLongestMatch(start, end, maxPossible)
	if matchPos == matchNotFound {
		l("ss Match not found\n")
		w.matchScanIndex++
		w.matchLength = 0
		return encodeStateYieldTagBit
	}
	l("ss Found match of %d bytes at %d\n", matchLength, matchPos)
	w.matchPosition = matchPos
	w.matchLength = matchLength
	l("ss 1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse) %d\n", 1<<w.window)
	l("ss match_pos %d\n", matchPos)
	if matchPos < (1 << w.window) {
		l("ss match_pos < (1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse)) 1\n")
	} else {
		l("ss match_pos < (1 << HEATSHRINK_ENCODER_WINDOW_BITS(hse)) 0\n")
	}

	return encodeStateYieldTagBit
}

func (w *Writer) stateYieldTagBit() (encodeState, error) {
	if w.matchLength == 0 {
		err := w.addTagBit(heatshrinkLiteralMarker)
		if err != nil {
			return encodeStateInvalid, err
		}
		return encodeStateYieldLiteral, nil
	}
	err := w.addTagBit(heatshrinkBackrefMarker)
	if err != nil {
		return encodeStateInvalid, err
	}
	w.outgoingBits = w.matchPosition - 1
	w.outgoingBitsCount = w.window
	return encodeStateYieldBackRefIndex, nil
}

func (w *Writer) stateYieldLiteral() (encodeState, error) {
	err := w.pushLiteralByte()
	if err != nil {
		return encodeStateInvalid, err
	}
	return encodeStateSearch, nil
}

func (w *Writer) stateYieldBackRefIndex() (encodeState, error) {
	l("-- yielding backref index %d\n", w.matchPosition)
	count, err := w.pushOutgoingBits()
	if err != nil {
		return encodeStateInvalid, err
	}
	if count > 0 {
		return encodeStateYieldBackRefIndex, nil
	}
	w.outgoingBits = w.matchLength - 1
	w.outgoingBitsCount = w.lookahead
	return encodeStateYieldBackRefLength, nil
}

func (w *Writer) stateYieldBackRefLength() (encodeState, error) {
	l("-- yielding backref length %d\n", w.matchLength)
	count, err := w.pushOutgoingBits()
	if err != nil {
		return encodeStateInvalid, err
	}
	if count > 0 {
		return encodeStateYieldBackRefLength, nil
	}
	w.matchScanIndex += w.matchLength
	w.matchLength = 0
	return encodeStateSearch, nil
}

func (w *Writer) stateSaveBacklog() encodeState {
	l("-- saving backlog\n")
	w.saveBacklog()
	return encodeStateNotFull
}

func (w *Writer) stateFlushBitBuffer() (encodeState, error) {
	if w.bitIndex == 0x80 {
		l("-- done!\n")
		return encodeStateDone, nil
	}
	l("-- flushing remaining byte (bit_index == 0x%02x)\n", w.bitIndex)
	err := w.w.WriteByte(w.current)
	if err != nil {
		l("-- error flushing: %v\n", err)
		return encodeStateInvalid, err
	}
	w.outputTotal++
	l("-- done!\n")
	return encodeStateDone, nil
}

const matchNotFound = ^int(0)

func (w *Writer) findLongestMatch(start int, end int, max int) (int, int) {
	l("-- scanning for match of buf[%d:%d] between buf[%d:%d] (max %d bytes)\n",
		end, end+max, start, end+max-1, max)
	var matchMaxLength int
	var matchIndex = matchNotFound
	var len int
	needlepoint := w.buffer[end:]
	pos := w.index[end]
	l("pos: %d\n", pos)

	for pos-int16(start) >= 0 {
		pospoint := w.buffer[pos:]
		len = 0
		l("  --> cmp buf[%d] == 0x%02x against %02x (start %d)\n", pos+int16(len), pospoint[len], needlepoint[len], start)
		if pospoint[matchMaxLength] != needlepoint[matchMaxLength] {
			pos = w.index[pos]
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
		pos = w.index[pos]
	}
	breakEven := 1 + w.window + w.lookahead
	if 8*uint(matchMaxLength) > uint(breakEven) {
		l("-- best match: %d bytes at -%d\n", matchMaxLength, end-matchIndex)
		return end - matchIndex, matchMaxLength
	}
	l("-- none found\n")
	return matchNotFound, 0
}

func (w *Writer) pushLiteralByte() error {
	processedOffset := w.matchScanIndex - 1
	inputOffset := w.getInputBufferSize() + processedOffset

	b := w.buffer[inputOffset]
	if isPrint(b) {
		l("-- yielded literal byte 0x%02x ('%c') from +%d\n", b, b, inputOffset)
	} else {
		l("-- yielded literal byte 0x%02x ('.') from +%d\n", b, inputOffset)
	}
	return w.pushBits(8, b)
}

func (w *Writer) pushBits(count uint8, bits byte) error {
	l("++ push_bits: %d bits, input of 0x%02x\n", count, bits)
	if count == 8 && w.bitIndex == 0x80 {
		err := w.w.WriteByte(bits)
		if err == nil {
			w.outputTotal++
		}
		return err
	}
	var i int16
	var err error
	for i = int16(count) - 1; i >= 0; i-- {
		bit := bits & (1 << uint16(i))
		if bit > 0 {
			w.current |= w.bitIndex
			l("  -- setting bit %d at bit index 0x%02x, byte => 0x%02x\n", 1, w.bitIndex, w.current)
		} else {
			l("  -- setting bit %d at bit index 0x%02x, byte => 0x%02x\n", 0, w.bitIndex, w.current)
		}
		w.bitIndex >>= 1
		if w.bitIndex == 0 {
			w.bitIndex = 0x80
			l(" > pushing byte 0x%02x\n", w.current)
			err = w.w.WriteByte(w.current)
			if err != nil {
				return err
			}
			w.outputTotal++
			w.current = 0x0
		}
	}
	return nil
}

func (w *Writer) pushOutgoingBits() (uint8, error) {
	var count uint8
	var bits byte
	if w.outgoingBitsCount > 8 {
		count = 8
		bits = byte(w.outgoingBits >> (w.outgoingBitsCount - 8))
	} else {
		count = w.outgoingBitsCount
		bits = byte(w.outgoingBits)
	}
	if count > 0 {
		l("-- pushing %d outgoing bits: 0x%02x\n", count, bits)
		err := w.pushBits(count, bits)
		if err != nil {
			return 0, err
		}
		w.outgoingBitsCount -= count
	}
	return count, nil
}

func (w *Writer) addTagBit(tag byte) error {
	l("-- adding tag bit: %d\n", tag)
	return w.pushBits(1, tag)
}

func (w *Writer) saveBacklog() {
	msi := w.matchScanIndex
	copy(w.buffer, w.buffer[msi:])
	w.matchScanIndex = 0
	w.inputSize -= msi
}

func (w *Writer) getInputBufferSize() int {
	return 1 << w.window
}

func (w *Writer) doIndexing() {
	var last [256]int16
	for i := range last {
		last[i] = -1
	}
	ibs := w.getInputBufferSize()
	end := ibs + w.inputSize
	var i int
	for i = 0; i < end; i++ {
		v := w.buffer[i]
		lv := last[v]
		l("-- setting index: %d, v: %d lv: %d\n", i, v, lv)
		w.index[i] = lv
		last[v] = int16(i)
	}
}

func (w *Writer) isFinishing() bool {
	return w.flags&encodeFlagsFinishing == encodeFlagsFinishing
}