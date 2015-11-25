package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/currantlabs/goheatshrink"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	encode  = kingpin.Flag("encode", "encode (compress, default)").Short('e').Default("true").Bool()
	decode  = kingpin.Flag("decode", "decode (decompress)").Short('d').Bool()
	verbose = kingpin.Flag("verbose", "verbose (print input & output sizes, compression ratio, etc.)").Short('v').Bool()

	window    = kingpin.Flag("window", "Base-2 log of LZSS sliding window size").Short('w').Default("8").Int()
	lookahead = kingpin.Flag("lookahead", "Number of bits used for back-reference lengths").Short('l').Default("4").Int()

	inFile  = kingpin.Arg("IN_FILE", "The file to process.").String()
	outFile = kingpin.Arg("OUT_FILE", "The file to write to").String()
)

func main() {

	kingpin.Version("0.1")
	kingpin.Parse()

	var s counter
	var writer io.WriteCloser
	var reader io.Reader

	var in *os.File
	var out *os.File
	var reporter *os.File

	if *inFile != "" && *outFile != "" {
		var err error
		in, err = os.Open(*inFile)
		if err != nil {
			log.Fatal(err)
		}
		defer in.Close()

		out, err = os.Create(*outFile)
		if err != nil {
			log.Fatal(err)
		}
		defer out.Close()

		if *verbose {
			reporter = os.Stdout
		}
	} else {
		in = os.Stdin
		out = os.Stdout
		if *verbose {
			reporter = os.Stderr
		}
	}
	if *decode {
		var ir io.Reader = in
		if *verbose {
			rs := &readSnoop{Reader: ir}
			s = rs
			ir = rs
		}
		writer = out
		reader = goheatshrink.NewReader(ir, goheatshrink.Window(uint8(*window)), goheatshrink.Lookahead(uint8(*lookahead)))
	} else if *encode {
		var wc io.WriteCloser = out
		if *verbose {
			ws := &writeSnoop{WriteCloser: wc}
			s = ws
			wc = ws
		}
		writer = goheatshrink.NewWriter(wc, goheatshrink.Window(uint8(*window)), goheatshrink.Lookahead(uint8(*lookahead)))
		reader = in
	} else {
		log.Fatal(errors.New("Must provide either encode or decode"))
	}
	process(reader, writer, reporter, *outFile, s, uint8(*window), uint8(*lookahead))
}

func process(in io.Reader, out io.WriteCloser, reporter *os.File, outFile string, s counter, w uint8, l uint8) {
	n, err := io.Copy(out, in)
	if err != nil {
		log.Fatal(err)
	}

	err = out.Close()
	if err != nil {
		log.Fatal(err)
	}

	if reporter != nil {
		reporter.WriteString(fmt.Sprintf("%s %0.2f%%\t %d -> %d (-w %d -l %d)\n", outFile, 100.0-(100.0*float64(s.Count()))/float64(n), n, s.Count(), w, l))
	}

}

type counter interface {
	Count() int64
}

type snoop struct {
	count int64
}

func (s *snoop) Count() int64 {
	return s.count
}

type readSnoop struct {
	snoop
	io.Reader
}

func (s *readSnoop) Read(p []byte) (n int, err error) {
	n, err = s.Reader.Read(p)
	s.count += int64(n)
	return
}

type writeSnoop struct {
	snoop
	io.WriteCloser
}

func (s *writeSnoop) Write(p []byte) (n int, err error) {
	n, err = s.WriteCloser.Write(p)
	s.count += int64(n)
	return
}
