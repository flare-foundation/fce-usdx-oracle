// Command cycles-web serves the keeper's cycle records, and the page that reads
// them, over HTTP.
//
//	cycles-web -records /var/log/usdx/cycles.jsonl -addr :8080
//
// It is meant to run beside a keeper — same pod, one volume between them — so
// that what an operator looks at is the file the keeper is writing, not a copy
// somebody exported. The keeper needs `-records <file>` for that; without the
// flag its records go to stdout and there is nothing here to serve.
//
// It holds nothing and decides nothing: two read-only routes over one file, no
// key, no chain client, no state. That is deliberate — it shares an image with
// the keeper but not its environment, so the funded key never enters this
// process. Consumers of the feed read the chain; this is for the people
// operating the machines.
//
// The page is embedded rather than mounted, so the image is the whole deployment
// and there is no ConfigMap to keep in step with it. `/cycles.jsonl` serves the
// most recent records, capped, so a file that has been growing for a month
// cannot turn one page load into a hundred-megabyte response.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

//go:embed index.html
var page []byte

// How far back into the file to read before splitting it into lines. The tail is
// bounded by bytes as well as by records because both are ways for a large file
// to hurt: at roughly 700 bytes a record, this covers far more than -max ever
// asks for, and it means a corrupt or enormous file costs one bounded read.
const maxScanBytes = 8 << 20

func main() {
	addrF := flag.String("addr", ":8080", "address to listen on")
	recordsF := flag.String("records", "/var/log/usdx/cycles.jsonl",
		"the keeper's -records file, as the keeper writes it")
	maxF := flag.Int("max", 2000,
		"how many of the most recent records to serve; a ten-minute interval puts a day in 144")
	flag.Parse()

	if *maxF < 1 {
		logger.Fatalf("-max must be at least 1")
	}

	mux := http.NewServeMux()

	// The page. Served at "/" only: anything else that reaches this handler is a
	// path that does not exist, and a page returned under it would look like it
	// worked.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", buildTime(), newBytesReader(page))
	})

	// The records. no-store because the page polls this: a cached answer would
	// show an operator a feed that has not moved when it has.
	mux.HandleFunc("/cycles.jsonl", func(w http.ResponseWriter, r *http.Request) {
		body, n, err := tail(*recordsF, *maxF)
		if err != nil {
			logger.Warnf("reading %s: %s", *recordsF, err)
			http.Error(w, "cannot read the records file", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		// How many lines the tail returned, for a curl or a probe that wants an
		// answer without reading the body. It counts lines rather than records —
		// the file holds nothing else, but this handler does not parse to find out.
		// The page decides "connected, nothing recorded yet" from the body: empty
		// is a legitimate answer until the first cycle ends.
		w.Header().Set("X-Usdx-Lines", strconv.Itoa(n))
		_, _ = w.Write(body)
	})

	// Liveness and readiness. The records file is deliberately not part of it:
	// this process is up whether or not the keeper has written its first cycle,
	// and a probe that waits for one would restart a healthy container for ten
	// minutes at every rollout.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})

	srv := &http.Server{
		Addr:              *addrF,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		// Short, because nothing here is worth waiting for: every request is a
		// read of one file, and a page that misses its poll asks again.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Infof("serving %s on %s", *recordsF, *addrF)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("listening on %s: %s", *addrF, err)
	}
	logger.Infof("stopped")
}

// tail returns the last max complete records of the file, oldest first, and how
// many it found. A missing file is not an error: the keeper writes it at the end
// of its first cycle, and until then an empty answer is the truth.
func tail(path string, max int) ([]byte, int, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}

	offset := int64(0)
	if info.Size() > maxScanBytes {
		offset = info.Size() - maxScanBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}

	// A seek into the middle of the file lands mid-record; that first partial
	// line is not a record and must not be served as one.
	if offset > 0 {
		if nl := indexByte(buf, '\n'); nl >= 0 {
			buf = buf[nl+1:]
		} else {
			buf = nil
		}
	}

	lines := splitLines(buf)
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}

	out := make([]byte, 0, len(buf))
	for _, line := range lines {
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out, len(lines), nil
}

// splitLines keeps the non-empty lines and drops a trailing partial one: the
// keeper appends with one write per record, but a read can still catch it
// between the record and its newline.
func splitLines(buf []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] != '\n' {
			continue
		}
		if line := trimCR(buf[start:i]); len(line) > 0 {
			lines = append(lines, line)
		}
		start = i + 1
	}
	return lines
}

func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// newBytesReader and buildTime keep http.ServeContent happy: it wants a
// ReadSeeker and a modification time, and an embedded page has neither. The
// binary's own mtime is the closest true answer — the page changed when the
// image was built.
func newBytesReader(b []byte) io.ReadSeeker { return &sliceReader{b: b} }

type sliceReader struct {
	b   []byte
	pos int64
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.pos >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += int64(n)
	return n, nil
}

func (r *sliceReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = int64(len(r.b)) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	r.pos = abs
	return abs, nil
}

func buildTime() time.Time {
	exe, err := os.Executable()
	if err != nil {
		return time.Time{}
	}
	info, err := os.Stat(exe)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
