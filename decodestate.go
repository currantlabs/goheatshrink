package goheatshrink

//go:generate stringer -type=DecodeState

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