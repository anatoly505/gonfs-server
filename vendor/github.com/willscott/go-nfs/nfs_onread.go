package nfs

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

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

// Throughput diagnostics — atomic counters, zero overhead when not read.
var (
	readStatOps   uint64
	readStatBytes uint64
	readStatFirst sync.Once
	readStatOnce  sync.Once
)

func startReadStats() {
	readStatOnce.Do(func() {
		go func() {
			for range time.Tick(5 * time.Second) {
				ops := atomic.SwapUint64(&readStatOps, 0)
				bytes := atomic.SwapUint64(&readStatBytes, 0)
				if ops > 0 {
					avgKB := bytes / ops / 1024
					mbps := float64(bytes) * 8 / 5 / 1_000_000
					Log.Infof("READ stats (5s): %d ops, avg %d KB/op, %.1f Mbps",
						ops, avgKB, mbps)
				}
			}
		}()
	})
}

func onRead(ctx context.Context, w *response, userHandle Handler) error {
	w.errorFmt = opAttrErrorFormatter
	var obj nfsReadArgs
	if err := xdr.Read(w.req.Body, &obj); err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}

	startReadStats()

	readStatFirst.Do(func() {
		Log.Infof(">>> Client rsize detected: %d bytes (%d KB). If this is small (<= 32 KB), throughput will be terrible. Client must mount with rsize=1048576.",
			obj.Count, obj.Count/1024)
	})

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

	atomic.AddUint64(&readStatOps, 1)
	atomic.AddUint64(&readStatBytes, uint64(cnt))

	var eof uint32
	if errors.Is(readErr, io.EOF) {
		eof = 1
	}

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
