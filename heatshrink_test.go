package goheatshrink

import (
	"testing"
	"crypto/rand"
	"log"
	"bytes"
	"io/ioutil"
)


func Test(t *testing.T) {
	testdata := make([]byte, 3760)
	rand.Read(testdata)
	e := NewEncoder(8, 4)
	log.Printf("Encoding...")
	encoded, err := e.Encode(testdata)
	if err != nil {
		t.Errorf("Error encoding: %v", err)
	}
	r := bytes.NewBuffer(encoded)
	d := NewReader(r, 8, 4)
	log.Printf("Decoding...")
	decoded, err := ioutil.ReadAll(d)
	if err != nil {
		t.Errorf("Error decoding: %v", err)
	}
	for i := range decoded {
		if testdata[i] != decoded[i] {
			t.Errorf("Different at: %d data -> %v decompressed -> %v", i, testdata[i], decoded[i])
			break
		}
	}

}
