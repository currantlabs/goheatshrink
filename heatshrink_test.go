package goheatshrink

import (
	"bytes"
	"crypto/rand"
	"io/ioutil"
	"log"
	"testing"
	"time"
)

func Test(t *testing.T) {
	testdata := make([]byte, 1 << 16)
	rand.Read(testdata)
	log.Printf("Encoding...")
	w := bytes.NewBuffer(testdata)
	var encoded bytes.Buffer
	e := NewWriter(&encoded, 8, 4)
	_, err := w.WriteTo(e)
	if err != nil {
		t.Errorf("Error encoding: %v", err)
	}
	r := bytes.NewBuffer(encoded.Bytes())
	d := NewReader(r, 8, 4)
	log.Printf("Decoding...")
	start := time.Now()
	decoded, err := ioutil.ReadAll(d)
	log.Printf("Decoded %v", time.Since(start))
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
