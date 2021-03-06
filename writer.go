package goheatshrink

import (
	"bufio"
	"errors"
	"io"
	"log"
)

type writer struct {
	*config

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
	index     []int16

	inner       inner
	outputTotal int
}

type inner interface {
	io.ByteWriter
	Flush() error
}

type encodeFlags int

const (
	encodeFlagsNone      encodeFlags = 0
	encodeFlagsFinishing             = 1
)

type encodeState int

const (
	encodeStateNotFull encodeState = iota
	encodeStateFilled
	encodeStateSearch
	encodeStateYieldTagBit
	encodeStateYieldLiteral
	encodeStateYieldBackRefIndex
	encodeStateYieldBackRefLength
	encodeStateSaveBacklog
	encodeStateFlushBits
	encodeStateDone
	encodeStateInvalid
)

const heatshrinkLiteralMarker byte = 0x01
const heatshrinkBackrefMarker byte = 0x00

// NewWriterConfig creates a new io.WriteCloser. Writes to the returned io.WriteCloser are compressed and written to w.
//
// config specifies the configuration values to use when compressing
//
// It is the caller's responsibility to call Close on the io.WriteCloser when done. Writes may be buffered and not flushed until Close.
func NewWriter(w io.Writer, options ...func(*config)) io.WriteCloser {
	hw := &writer{
		config: &config{window:defaultWindow, lookahead:defaultLookahead},
		state: encodeStateNotFull,
		bitIndex: 0x80,
	}
	for _, option := range options {
		option(hw.config)
	}
	bufSize := 2 << hw.window
	bw, ok := w.(inner)
	if !ok {
		bw = bufio.NewWriter(w)
	}
	hw.inner = bw
	hw.buffer = make([]byte, bufSize)
	hw.index = make([]int16, bufSize)
	return hw
}

func (w *writer) Write(p []byte) (n int, err error) {
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

		outputSize, err := w.poll()
		if err != nil {
			return done, err
		}
		if done >= total {
			break
		}

		if w.state == encodeStateNotFull {
			continue
		}

		if outputSize == 0 {
			if w.finish() {
				return done, nil
			}
		}

	}
	return done, nil
}

func (w *writer) Close() error {
	var err error
	if w.finish() {
		err = w.inner.Flush()
		if err != nil {
			return err
		}
		return nil
	}
	_, err = w.poll()
	if err != nil {
		return err
	}
	if w.finish() {
		err = w.inner.Flush()
		if err != nil {
			return err
		}
		return nil
	}
	return ErrBadStateOnClose
}

func (w *writer) sink(in []byte) (int, error) {
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

func (w *writer) poll() (int, error) {

	w.outputTotal = 0
	var err error

	for {
		state := w.state
		switch state {
		case encodeStateNotFull:
			return w.outputTotal, nil
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
			return w.outputTotal, nil
		case encodeStateInvalid:
			log.Fatal("Invalid state: %v", state)
		default:
			log.Fatal("Unknown state: %v", state)
		}
		if err != nil {
			return w.outputTotal, err
		}

	}

}

func (w *writer) finish() bool {
	w.flags |= encodeFlagsFinishing
	if w.state == encodeStateNotFull {
		w.state = encodeStateFilled
	}
	return w.state == encodeStateDone
}

func (w *writer) stateStepSearch() encodeState {
	windowLength := 1 << w.window
	lookaheadLength := 1 << w.lookahead
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

func (w *writer) stateYieldTagBit() (encodeState, error) {
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

func (w *writer) stateYieldLiteral() (encodeState, error) {
	err := w.pushLiteralByte()
	if err != nil {
		return encodeStateInvalid, err
	}
	return encodeStateSearch, nil
}

func (w *writer) stateYieldBackRefIndex() (encodeState, error) {
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

func (w *writer) stateYieldBackRefLength() (encodeState, error) {
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

func (w *writer) stateSaveBacklog() encodeState {
	w.saveBacklog()
	return encodeStateNotFull
}

func (w *writer) stateFlushBitBuffer() (encodeState, error) {
	if w.bitIndex == 0x80 {
		return encodeStateDone, nil
	}
	err := w.inner.WriteByte(w.current)
	if err != nil {
		return encodeStateInvalid, err
	}
	w.outputTotal++
	return encodeStateDone, nil
}

const matchNotFound = ^int(0)

func (w *writer) findLongestMatch(start int, end int, max int) (int, int) {
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

func (w *writer) pushLiteralByte() error {
	processedOffset := w.matchScanIndex - 1
	inputOffset := w.getInputBufferSize() + processedOffset

	b := w.buffer[inputOffset]
	return w.pushBits(8, b)
}

func (w *writer) pushBits(count uint8, bits byte) error {
	if count == 8 && w.bitIndex == 0x80 {
		err := w.inner.WriteByte(bits)
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
			err = w.inner.WriteByte(w.current)
			if err != nil {
				return err
			}
			w.outputTotal++
			w.current = 0x0
		}
	}
	return nil
}

func (w *writer) pushOutgoingBits() (uint8, error) {
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

func (w *writer) addTagBit(tag byte) error {
	return w.pushBits(1, tag)
}

func (w *writer) saveBacklog() {
	msi := w.matchScanIndex
	copy(w.buffer, w.buffer[msi:])
	w.matchScanIndex = 0
	w.inputSize -= msi
}

func (w *writer) getInputBufferSize() int {
	return 1 << w.window
}

func (w *writer) doIndexing() {
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

func (w *writer) isFinishing() bool {
	return w.flags&encodeFlagsFinishing == encodeFlagsFinishing
}
