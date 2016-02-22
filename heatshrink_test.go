package goheatshrink

import (
	"bytes"
	"flag"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	rand.Seed(time.Now().UnixNano())
	flag.Parse()
	os.Exit(m.Run())
}

func TestDataWithoutDuplication(t *testing.T) {
	d := []byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i',
		'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r',
		's', 't', 'u', 'v', 'w', 'x', 'y', 'z'}
	testRoundTrip(t, d, 8, 4)
}

func TestDataWithDuplication(t *testing.T) {
	d := []byte{'a', 'b', 'c', 'a', 'b', 'c', 'd', 'a', 'b',
		'c', 'd', 'e', 'a', 'b', 'c', 'd', 'e', 'f',
		'a', 'b', 'c', 'd', 'e', 'f', 'g', 'a', 'b',
		'c', 'd', 'e', 'f', 'g', 'h'}
	testRoundTrip(t, d, 8, 4)
}

func TestDataWithoutDuplicationTinyBuffers(t *testing.T) {
	d := []byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i',
		'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r',
		's', 't', 'u', 'v', 'w', 'x', 'y', 'z'}
	testRoundTripTinyBuffers(t, d, 8, 4)
}

func BenchmarkRoundTrip(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		testdata := random(1 << 16)
		testRoundTrip(b, testdata, 8, 4)
	}
}

func TestRandom(t *testing.T) {
	testdata := random(1 << 16)
	testRoundTrip(t, testdata, 8, 4)
}

func TestRandomBigWindow(t *testing.T) {
	testdata := random(1 << 16)
	testRoundTrip(t, testdata, 16, 4)
}

func testRoundTrip(t testing.TB, testdata []byte, window uint8, lookahead uint8) {
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

func testRoundTripTinyBuffers(t testing.TB, testdata []byte, window uint8, lookahead uint8) {
	compressed, err := compressTinyBuffers(testdata, window, lookahead)
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
	w := bytes.NewBuffer(in)
	var encoded bytes.Buffer
	e := NewWriter(&encoded, Window(window), Lookahead(lookahead))
	_, err := io.Copy(e, w)
	e.Close()
	if err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func compressTinyBuffers(in []byte, window uint8, lookahead uint8) ([]byte, error) {
	var encoded bytes.Buffer
	e := NewWriter(&encoded, Window(window), Lookahead(lookahead))
	total := 0
	for i := 0; i < len(in); i++ {
		n, err := e.Write(in[i : i+1])
		total += n
		if err != nil {
			return nil, err
		}
	}

	e.Close()
	return encoded.Bytes(), nil
}

func decompress(in []byte, window uint8, lookahead uint8) ([]byte, error) {
	r := bytes.NewBuffer(in)
	d := NewReader(r, Window(window), Lookahead(lookahead))
	decoded, err := ioutil.ReadAll(d)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func random(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.Int31n(256))
	}
	return b
}
