package goheatshrink

import (
	"testing"
	"crypto/rand"
)


func Test(t *testing.T) {
	testdata := make([]byte, 1 << 16)
	rand.Read(testdata)
	e := NewEncoder(8, 4)
	encoded, err := e.Encode(testdata)
	if err != nil {
		t.Errorf("Error encoding: %v", err)
	}
	d := NewDecoder(8, 4)
	decoded, err := d.Decode(encoded)
	if err != nil {
		t.Errorf("Error decoding: %v", err)
	}
	for i := range decoded {
		if testdata[i] != decoded[i] {
			t.Errorf("Different at: ", i, " data -> ", testdata[i], " decompressed -> ", decoded[i])
			break
		}
	}

}
