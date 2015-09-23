# goheatshrink

A port of the [heatshrink](https://github.com/atomicobject/heatshrink) embedded compression library to Go

## Examples

### Compressing

```go
package main

import (
    "io"
	"os"

	"github.com/currantlabs/goheatshrink"
)

func main() {
    in, _ := os.Open("bigfile.bin")
    defer in.Close()

    out, _ := os.Create("bigfile.bin.hz")
    defer out.Close()

    w := goheatshrink.NewWriter(out)

    io.Copy(w, in)
    w.Close()
}
```

### Decompressing

```go
package main

import (
    "io"
	"os"

	"github.com/currantlabs/goheatshrink"
)

func main() {
    in, _ := os.Open("bigfile.bin.hz")
    defer in.Close()

    out, _ := os.Create("bigfile.bin")
    defer out.Close()

    r := goheatshrink.NewReader(in)

    io.Copy(out, r)
}
```

## Build Status

  [![Build Status](https://travis-ci.org/currantlabs/goheatshrink.png)](http://travis-ci.org/currantlabs/goheatshrink)