package goheatshrink

//go:generate stringer -type=decodeState

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
