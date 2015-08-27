package goheatshrink

//go:generate stringer -type=encodeState

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
