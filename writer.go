package goheatshrink

import (
	"bufio"
	"errors"
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
	offset := w.getInputBufferSize() + w.inputSize
	ibs := w.getInputBufferSize()
	remaining := ibs - w.inputSize
	var copySize int
	if remaining < len(in) {
		copySize = remaining
	} else {
		copySize = len(in)
	}
	copy(w.buffer[offset:], in[:copySize])
	w.inputSize += copySize
	if copySize == remaining {
		w.state = encodeStateFilled
	}
	return copySize, nil
}

func (w *Writer) poll() (int, error, bool) {

	w.outputTotal = 0
	var err error

	for {
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
	fin := w.isFinishing()
	var lookaheadCompare int
	if fin {
		lookaheadCompare = 1
	} else {
		lookaheadCompare = lookaheadLength
	}
	if msi > w.inputSize-lookaheadCompare {
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
		w.matchScanIndex++
		w.matchLength = 0
		return encodeStateYieldTagBit
	}
	w.matchPosition = matchPos
	w.matchLength = matchLength
	if matchPos < (1 << w.window) {
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
	w.saveBacklog()
	return encodeStateNotFull
}

func (w *Writer) stateFlushBitBuffer() (encodeState, error) {
	if w.bitIndex == 0x80 {
		return encodeStateDone, nil
	}
	err := w.w.WriteByte(w.current)
	if err != nil {
		return encodeStateInvalid, err
	}
	w.outputTotal++
	return encodeStateDone, nil
}

const matchNotFound = ^int(0)

func (w *Writer) findLongestMatch(start int, end int, max int) (int, int) {
	var matchMaxLength int
	var matchIndex = matchNotFound
	var len int
	needlepoint := w.buffer[end:]
	pos := w.index[end]

	for pos-int16(start) >= 0 {
		pospoint := w.buffer[pos:]
		len = 0
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
		return end - matchIndex, matchMaxLength
	}
	return matchNotFound, 0
}

func (w *Writer) pushLiteralByte() error {
	processedOffset := w.matchScanIndex - 1
	inputOffset := w.getInputBufferSize() + processedOffset

	b := w.buffer[inputOffset]
	return w.pushBits(8, b)
}

func (w *Writer) pushBits(count uint8, bits byte) error {
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
		}
		w.bitIndex >>= 1
		if w.bitIndex == 0 {
			w.bitIndex = 0x80
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
		err := w.pushBits(count, bits)
		if err != nil {
			return 0, err
		}
		w.outgoingBitsCount -= count
	}
	return count, nil
}

func (w *Writer) addTagBit(tag byte) error {
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
		w.index[i] = lv
		last[v] = int16(i)
	}
}

func (w *Writer) isFinishing() bool {
	return w.flags&encodeFlagsFinishing == encodeFlagsFinishing
}
