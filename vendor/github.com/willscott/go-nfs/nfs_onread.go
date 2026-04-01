package nfs

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/willscott/go-nfs-client/nfs/xdr"
)

type nfsReadArgs struct {
	Handle []byte
	Offset uint64
	Count  uint32
}

const MaxRead = 1 << 24

var readBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 1<<20)
		return &b
	},
}

func getReadBuf(size int) []byte {
	bp := readBufPool.Get().(*[]byte)
	b := *bp
	if cap(b) < size {
		b = make([]byte, size)
	}
	return b[:size]
}

func putReadBuf(b []byte) {
	b = b[:cap(b)]
	readBufPool.Put(&b)
}

func onRead(ctx context.Context, w *response, userHandle Handler) error {
	w.errorFmt = opAttrErrorFormatter
	var obj nfsReadArgs
	if err := xdr.Read(w.req.Body, &obj); err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}

	fs, path, err := userHandle.FromHandle(obj.Handle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}

	fh, err := fs.Open(fs.Join(path...))
	if err != nil {
		if os.IsNotExist(err) {
			return &NFSStatusError{NFSStatusNoEnt, err}
		}
		return &NFSStatusError{NFSStatusAccess, err}
	}
	defer fh.Close()

	if obj.Count > MaxRead {
		obj.Count = MaxRead
	}

	buf := getReadBuf(int(obj.Count))
	defer putReadBuf(buf)

	cnt, readErr := fh.ReadAt(buf, int64(obj.Offset))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return &NFSStatusError{NFSStatusIO, readErr}
	}

	var eof uint32
	if errors.Is(readErr, io.EOF) {
		eof = 1
	}

	// All I/O complete — build response directly into the response writer,
	// eliminating the intermediate bytes.Buffer and one full data copy.
	if err := w.writeHeader(ResponseCodeSuccess); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	xdr.Write(w.writer, uint32(NFSStatusOk))
	WritePostOpAttrs(w.writer, tryStat(fs, path))

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(cnt))
	w.writer.Write(hdr[:])
	binary.BigEndian.PutUint32(hdr[:], eof)
	w.writer.Write(hdr[:])
	binary.BigEndian.PutUint32(hdr[:], uint32(cnt))
	w.writer.Write(hdr[:])
	w.writer.Write(buf[:cnt])
	if pad := cnt % 4; pad != 0 {
		var zeros [3]byte
		w.writer.Write(zeros[:4-pad])
	}

	return nil
}
