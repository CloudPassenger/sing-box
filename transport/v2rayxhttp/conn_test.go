package xhttp

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/xray/signal/done"
	D "github.com/sagernet/sing/common/bufio/deadline"
)

func TestSplitConnServerDirectionalDeadlines(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			controller := newBlockingResponseController(operation)
			serverConn := &httpServerConn{
				Instance:       done.New(),
				Reader:         controller,
				ResponseWriter: controller,
			}
			var closeCallbacks atomic.Int32
			conn := &splitConn{
				reader:  serverConn,
				writer:  serverConn,
				onClose: func() { closeCallbacks.Add(1) },
			}
			if conn.NeedAdditionalReadDeadline() {
				t.Fatal("server connection with ResponseController deadlines requested the deadline adapter")
			}
			result := make(chan error, 1)
			go func() {
				if operation == "read" {
					_, err := conn.Read(make([]byte, 1))
					result <- err
					return
				}
				_, err := conn.Write([]byte{1})
				result <- err
			}()

			waitClosed(t, controller.started, "I/O start")
			if operation == "read" {
				if err := conn.SetReadDeadline(time.Now()); err != nil {
					t.Fatalf("set read deadline: %v", err)
				}
			} else {
				if err := conn.SetWriteDeadline(time.Now()); err != nil {
					t.Fatalf("set write deadline: %v", err)
				}
			}
			if err := waitError(t, result); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("%s error = %v, want deadline exceeded", operation, err)
			}
			if serverConn.Done() {
				t.Fatal("deadline performed owner Close")
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("owner close: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("repeated owner close: %v", err)
			}
			if got := closeCallbacks.Load(); got != 1 {
				t.Fatalf("close callbacks = %d, want 1", got)
			}
		})
	}
}

func TestSplitConnUnsupportedServerReadDeadlineUsesAdapter(t *testing.T) {
	t.Parallel()
	serverConn := &httpServerConn{
		Instance:       done.New(),
		Reader:         http.NoBody,
		ResponseWriter: httptest.NewRecorder(),
	}
	conn := &splitConn{reader: serverConn, writer: serverConn}
	if !conn.NeedAdditionalReadDeadline() {
		t.Fatal("server connection without ResponseController read deadlines did not request the deadline adapter")
	}
	if err := conn.SetReadDeadline(time.Now()); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("set unsupported read deadline error = %v, want http.ErrNotSupported", err)
	}
}

func TestSplitConnAdditionalReadDeadlineReleasesRouteOwner(t *testing.T) {
	t.Parallel()
	reader := newBlockingReadCloser()
	writer := &countingWriteCloser{}
	conn := &splitConn{reader: reader, writer: writer}
	if !conn.NeedAdditionalReadDeadline() {
		t.Fatal("split connection without source read deadlines must request the deadline adapter")
	}
	wrapped := D.NewConn(conn)
	result := make(chan error, 1)
	go func() {
		_, err := wrapped.Read(make([]byte, 1))
		result <- err
	}()

	waitClosed(t, reader.started, "underlying read start")
	if err := wrapped.SetReadDeadline(time.Now()); err != nil {
		t.Fatalf("set adapted read deadline: %v", err)
	}
	if err := waitError(t, result); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("adapted read error = %v, want deadline exceeded", err)
	}
	if got := reader.closeCalls.Load(); got != 0 {
		t.Fatalf("reader close calls before owner Close = %d, want 0", got)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("owner close: %v", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("repeated owner close: %v", err)
	}
	waitClosed(t, reader.finished, "underlying read completion")
	if got := reader.closeCalls.Load(); got != 1 {
		t.Fatalf("reader close calls = %d, want 1", got)
	}
	if got := writer.closeCalls.Load(); got != 1 {
		t.Fatalf("writer close calls = %d, want 1", got)
	}
}

func TestSplitConnWriteDeadlineInterruptsWithoutClose(t *testing.T) {
	t.Parallel()
	writer := newBlockingInterruptWriter()
	reader := &countingReadCloser{}
	conn := &splitConn{
		reader: reader,
		writer: newDeadlineWriteCloser(writer),
	}
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte{1})
		result <- err
	}()

	waitClosed(t, writer.started, "underlying write start")
	if err := conn.SetWriteDeadline(time.Now()); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if err := waitError(t, result); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write error = %v, want deadline exceeded", err)
	}
	if got := writer.closeCalls.Load(); got != 0 {
		t.Fatalf("writer close calls before owner Close = %d, want 0", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("owner close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("repeated owner close: %v", err)
	}
	if got := writer.closeCalls.Load(); got != 1 {
		t.Fatalf("writer close calls = %d, want 1", got)
	}
	if got := reader.closeCalls.Load(); got != 1 {
		t.Fatalf("reader close calls = %d, want 1", got)
	}
}

func TestDeadlineWriteCloserConcurrentResetAndClose(t *testing.T) {
	t.Parallel()
	writer := &countingInterruptWriter{}
	wrapper := newDeadlineWriteCloser(writer)
	if err := wrapper.SetWriteDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set future write deadline: %v", err)
	}
	if err := wrapper.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("clear write deadline: %v", err)
	}

	const setterCount = 32
	start := make(chan struct{})
	setResults := make(chan error, setterCount)
	var waitGroup sync.WaitGroup
	for index := range setterCount {
		waitGroup.Add(1)
		go func(clear bool) {
			defer waitGroup.Done()
			<-start
			deadline := time.Now().Add(time.Hour)
			if clear {
				deadline = time.Time{}
			}
			setResults <- wrapper.SetWriteDeadline(deadline)
		}(index%2 == 0)
	}
	closeResult := make(chan error, 1)
	waitGroup.Go(func() {
		<-start
		closeResult <- wrapper.Close()
	})
	close(start)
	waitGroup.Wait()
	close(setResults)

	if err := <-closeResult; err != nil {
		t.Fatalf("close deadline writer: %v", err)
	}
	for err := range setResults {
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("concurrent set write deadline error = %v, want nil or net.ErrClosed", err)
		}
	}
	if got := writer.interruptCalls.Load(); got != 0 {
		t.Fatalf("interrupt calls = %d, want 0", got)
	}
	if got := writer.closeCalls.Load(); got != 1 {
		t.Fatalf("close calls = %d, want 1", got)
	}
	if err := wrapper.SetWriteDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("set write deadline after close error = %v, want net.ErrClosed", err)
	}
}

func TestPipeInterruptWriterOwnerCloseAfterInterrupt(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	defer reader.Close()
	interruptWriter := &pipeInterruptWriter{PipeWriter: writer}
	interruptWriter.Interrupt()
	if err := interruptWriter.Close(); err != nil {
		t.Fatalf("owner close after interrupt: %v", err)
	}
	if err := interruptWriter.Close(); err != nil {
		t.Fatalf("repeated owner close after interrupt: %v", err)
	}
}

func TestSplitConnConcurrentCloseOnceJoinsErrors(t *testing.T) {
	t.Parallel()
	readErr := errors.New("read close")
	writeErr := errors.New("write close")
	reader := &errorReadCloser{err: readErr}
	writer := &errorWriteCloser{err: writeErr}
	var closeCallbacks atomic.Int32
	conn := &splitConn{
		reader:  reader,
		writer:  writer,
		onClose: func() { closeCallbacks.Add(1) },
	}

	const closerCount = 32
	start := make(chan struct{})
	results := make(chan error, closerCount)
	var waitGroup sync.WaitGroup
	for range closerCount {
		waitGroup.Go(func() {
			<-start
			results <- conn.Close()
		})
	}
	close(start)
	waitGroup.Wait()
	close(results)

	for err := range results {
		if !errors.Is(err, readErr) || !errors.Is(err, writeErr) {
			t.Fatalf("close error = %v, want both close errors", err)
		}
	}
	if got := reader.closeCalls.Load(); got != 1 {
		t.Fatalf("reader close calls = %d, want 1", got)
	}
	if got := writer.closeCalls.Load(); got != 1 {
		t.Fatalf("writer close calls = %d, want 1", got)
	}
	if got := closeCallbacks.Load(); got != 1 {
		t.Fatalf("close callbacks = %d, want 1", got)
	}
}

type blockingResponseController struct {
	operation    string
	header       http.Header
	started      chan struct{}
	deadline     chan struct{}
	startOnce    sync.Once
	deadlineOnce sync.Once
}

func newBlockingResponseController(operation string) *blockingResponseController {
	return &blockingResponseController{
		operation: operation,
		header:    make(http.Header),
		started:   make(chan struct{}),
		deadline:  make(chan struct{}),
	}
}

func (c *blockingResponseController) Header() http.Header {
	return c.header
}

func (c *blockingResponseController) WriteHeader(_ int) {}

func (c *blockingResponseController) Read(_ []byte) (int, error) {
	c.startOnce.Do(func() { close(c.started) })
	<-c.deadline
	return 0, os.ErrDeadlineExceeded
}

func (c *blockingResponseController) Write(_ []byte) (int, error) {
	c.startOnce.Do(func() { close(c.started) })
	<-c.deadline
	return 0, os.ErrDeadlineExceeded
}

func (c *blockingResponseController) SetReadDeadline(_ time.Time) error {
	if c.operation == "read" {
		c.deadlineOnce.Do(func() { close(c.deadline) })
	}
	return nil
}

func (c *blockingResponseController) SetWriteDeadline(_ time.Time) error {
	if c.operation == "write" {
		c.deadlineOnce.Do(func() { close(c.deadline) })
	}
	return nil
}

type blockingReadCloser struct {
	started    chan struct{}
	closed     chan struct{}
	finished   chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
	finishOnce sync.Once
	closeCalls atomic.Int32
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		started:  make(chan struct{}),
		closed:   make(chan struct{}),
		finished: make(chan struct{}),
	}
}

func (r *blockingReadCloser) Read(_ []byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.closed
	r.finishOnce.Do(func() { close(r.finished) })
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	r.closeCalls.Add(1)
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

type countingReadCloser struct {
	closeCalls atomic.Int32
}

func (r *countingReadCloser) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (r *countingReadCloser) Close() error {
	r.closeCalls.Add(1)
	return nil
}

type countingWriteCloser struct {
	closeCalls atomic.Int32
}

func (w *countingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *countingWriteCloser) Close() error {
	w.closeCalls.Add(1)
	return nil
}

type blockingInterruptWriter struct {
	started       chan struct{}
	interrupted   chan struct{}
	startOnce     sync.Once
	interruptOnce sync.Once
	closeCalls    atomic.Int32
}

func newBlockingInterruptWriter() *blockingInterruptWriter {
	return &blockingInterruptWriter{
		started:     make(chan struct{}),
		interrupted: make(chan struct{}),
	}
}

func (w *blockingInterruptWriter) Write(_ []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.interrupted
	return 0, io.ErrClosedPipe
}

func (w *blockingInterruptWriter) Interrupt() {
	w.interruptOnce.Do(func() { close(w.interrupted) })
}

func (w *blockingInterruptWriter) Close() error {
	w.closeCalls.Add(1)
	return nil
}

type countingInterruptWriter struct {
	interruptCalls atomic.Int32
	closeCalls     atomic.Int32
}

func (w *countingInterruptWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *countingInterruptWriter) Interrupt() {
	w.interruptCalls.Add(1)
}

func (w *countingInterruptWriter) Close() error {
	w.closeCalls.Add(1)
	return nil
}

type errorReadCloser struct {
	err        error
	closeCalls atomic.Int32
}

func (r *errorReadCloser) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (r *errorReadCloser) Close() error {
	r.closeCalls.Add(1)
	return r.err
}

type errorWriteCloser struct {
	err        error
	closeCalls atomic.Int32
}

func (w *errorWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *errorWriteCloser) Close() error {
	w.closeCalls.Add(1)
	return w.err
}

func waitClosed(t *testing.T, channel <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func waitError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for I/O result")
		return nil
	}
}
