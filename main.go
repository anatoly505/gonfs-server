package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

func main() {
	port := flag.Int("port", 2049, "TCP port")
	root := flag.String("root", ".", "Directory to export")
	readOnly := flag.Bool("ro", true, "Read-only mode")
	fdCache := flag.Int("fdcache", 256, "Open file descriptor cache size")
	handleCache := flag.Int("hcache", 2048, "NFS handle cache size")
	sendKB := flag.Int("sndbuf", 4096, "TCP send buffer size in KB")
	recvKB := flag.Int("rcvbuf", 2048, "TCP receive buffer size in KB")
	statSec := flag.Int("statttl", 5, "Stat cache TTL in seconds")
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatalf("Bad root path: %v", err)
	}
	if info, err := os.Stat(absRoot); err != nil || !info.IsDir() {
		log.Fatalf("%q is not a valid directory", absRoot)
	}

	listener, err := newTunedListener(*port, *sendKB*1024, *recvKB*1024)
	if err != nil {
		log.Fatalf("Listen failed: %v", err)
	}

	innerFS := osfs.New(absRoot, osfs.WithBoundOS())
	cachedFS := NewCachedFS(innerFS, *fdCache, time.Duration(*statSec)*time.Second)

	handler := NewFSHandler(cachedFS, absRoot, *readOnly)
	cached := nfshelper.NewCachingHandler(handler, *handleCache)

	log.Printf("NFS v3 server (streaming-optimized)")
	log.Printf("  Export:       %s", absRoot)
	log.Printf("  Listen:       :%d", *port)
	log.Printf("  Read-only:    %v", *readOnly)
	log.Printf("  FD cache:     %d entries", *fdCache)
	log.Printf("  Handle cache: %d entries", *handleCache)
	log.Printf("  TCP send buf: %d KB", *sendKB)
	log.Printf("  TCP recv buf: %d KB", *recvKB)
	log.Printf("  Stat TTL:     %d s", *statSec)
	log.Printf("  CPUs:         %d", runtime.NumCPU())
	log.Printf("")
	log.Printf("  Mount (Linux):")
	log.Printf("  mount -t nfs -o port=%d,mountport=%d,nfsvers=3,noacl,tcp,rsize=1048576,wsize=1048576 <host>:/ <dir>", *port, *port)

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		log.Println("Shutting down...")
		listener.Close()
		os.Exit(0)
	}()

	if err := nfs.Serve(listener, cached); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// tunedListener applies per-connection TCP optimizations critical for
// sustained high-throughput streaming of large files.
type tunedListener struct {
	*net.TCPListener
	sendBuf int
	recvBuf int
}

func newTunedListener(port, sendBuf, recvBuf int) (*tunedListener, error) {
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &tunedListener{TCPListener: l, sendBuf: sendBuf, recvBuf: recvBuf}, nil
}

func (l *tunedListener) Accept() (net.Conn, error) {
	tc, err := l.TCPListener.AcceptTCP()
	if err != nil {
		return nil, err
	}
	tc.SetNoDelay(true)
	tc.SetWriteBuffer(l.sendBuf)
	tc.SetReadBuffer(l.recvBuf)
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(30 * time.Second)
	return tc, nil
}
