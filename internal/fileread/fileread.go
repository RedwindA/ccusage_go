// Package fileread reads whole files into reused buffers. Loaders parse
// tens of megabytes of transcripts per run; fresh buffers for every file
// would grow the heap by that much, and in a short-lived process every new
// page costs a fault and kernel zeroing.
package fileread

import (
	"io"
	"os"
	"sync"
)

var buffers = sync.Pool{New: func() any { return new([]byte) }}

// Read returns the contents of the file at path in a reused buffer. Call
// release once neither data nor anything aliasing it is needed any more;
// copies (decoded strings are copies) may be kept.
func Read(path string) (data []byte, release func(), err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	bp := buffers.Get().(*[]byte)
	release = func() { buffers.Put(bp) }
	size := 0
	if info, err := file.Stat(); err == nil && info.Size() > 0 && int64(int(info.Size())) == info.Size() {
		size = int(info.Size())
	}
	buf := (*bp)[:0]
	if cap(buf) < size+1 { // +1 reaches EOF without growing
		buf = make([]byte, 0, size+1)
	}
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := file.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		*bp = buf
		if err == io.EOF {
			return buf, release, nil
		}
		if err != nil {
			release()
			return nil, nil, err
		}
	}
}
