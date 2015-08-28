package goheatshrink

import (
	"bytes"
	"crypto/rand"
	"io"
	"io/ioutil"
	"log"
	"testing"
	"time"
)

func TestRandom(t *testing.T) {
	testdata := make([]byte, 1<<16)
	rand.Read(testdata)
	testRoundTrip(t, testdata, 8, 4)
}

func TestRandomBigWindow(t *testing.T) {
	testdata := make([]byte, 1<<16)
	rand.Read(testdata)
	testRoundTrip(t, testdata, 16, 4)
}

func testRoundTrip(t *testing.T, testdata []byte, window uint8, lookahead uint8) {
	compressed, err := compress(testdata, window, lookahead)
	if err != nil {
		t.Errorf("Error compressing: %v", err)
		return
	}

	decompressed, err := decompress(compressed, window, lookahead)
	if err != nil {
		t.Errorf("Error decompressing: %v", err)
		return
	}

	if len(testdata) != len(decompressed) {
		t.Errorf("Different lengths: data -> %d decompressed -> %d", len(testdata), len(decompressed))
	}
	for i := range decompressed {
		if testdata[i] != decompressed[i] {
			t.Errorf("Different at: %d data -> %v decompressed -> %v", i, testdata[i], decompressed[i])
			break
		}
	}
}

func compress(in []byte, window uint8, lookahead uint8) ([]byte, error) {
	log.Printf("Compressing...")
	w := bytes.NewBuffer(in)
	var encoded bytes.Buffer
	e := NewWriterConfig(&encoded, &Config{Window: window, Lookahead: lookahead})
	start := time.Now()
	n, err := io.Copy(e, w)
	e.Close()
	if err != nil {
		return nil, err
	}
	log.Printf("Compressed %v bytes to %v in %v", n, encoded.Len(), time.Since(start))
	return encoded.Bytes(), nil
}

func decompress(in []byte, window uint8, lookahead uint8) ([]byte, error) {
	log.Printf("Dempressing...")
	r := bytes.NewBuffer(in)
	d := NewReaderConfig(r, &Config{Window: window, Lookahead: lookahead})
	start := time.Now()
	decoded, err := ioutil.ReadAll(d)
	if err != nil {
		return nil, err
	}
	log.Printf("Decompressed %v bytes to %v in %v", len(in), len(decoded), time.Since(start))
	return decoded, nil
}
