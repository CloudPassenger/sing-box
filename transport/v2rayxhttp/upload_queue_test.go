package xhttp

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/xray/signal/done"
)

func TestUploadQueueRegressionReadZero(t *testing.T) {
	t.Parallel()
	q := NewUploadQueue(10)
	err := q.Push(Packet{
		Payload: []byte("x"),
		Seq:     0,
	})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 20)
	n, err := q.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("n=", n)
	}
}

// TestUploadQueuePushFullThenClose reproduces the deadlock fixed upstream by
// Xray-core #6372: Push blocking on a full channel must never prevent Close
// from completing.
func TestUploadQueuePushFullThenClose(t *testing.T) {
	t.Parallel()
	q := NewUploadQueue(1)
	if err := q.Push(Packet{Payload: []byte("a"), Seq: 0}); err != nil {
		t.Fatal(err)
	}

	pushBlocked := make(chan error, 1)
	go func() {
		// the channel (capacity 1) is already full, so this Push blocks
		// until Close unblocks it.
		pushBlocked <- q.Push(Packet{Payload: []byte("b"), Seq: 1})
	}()

	// give the goroutine a chance to actually block inside the channel send.
	time.Sleep(20 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		if err := q.Close(); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return: deadlock")
	}

	select {
	case <-pushBlocked:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Push() did not return after Close(): deadlock")
	}
}

// TestUploadQueueConcurrentPushClose exercises many concurrent Push/Close
// calls with go test -race to catch ordering/visibility bugs in the reader
// hand-off (extended's intermediate mutex-based fix left a race here).
func TestUploadQueueConcurrentPushClose(t *testing.T) {
	t.Parallel()
	for range 50 {
		q := NewUploadQueue(4)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func(seq uint64) {
				defer wg.Done()
				_ = q.Push(Packet{Payload: []byte("x"), Seq: seq})
			}(uint64(i))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = q.Close()
		}()
		allDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(allDone)
		}()
		select {
		case <-allDone:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Push/Close did not complete: deadlock")
		}
	}
}

// TestUploadQueueCloseClosesInjectedReader verifies Close() releases a
// reader injected via Push (the stream-up/packet-up "Reader" packet), and
// that a second Push after the reader is set is rejected instead of
// silently accepted.
func TestUploadQueueCloseClosesInjectedReader(t *testing.T) {
	t.Parallel()
	q := NewUploadQueue(4)
	sc := &httpServerConn{
		Instance:       done.New(),
		ResponseWriter: httptest.NewRecorder(),
	}
	if err := q.Push(Packet{Reader: sc}); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(Packet{Payload: []byte("late")}); err == nil {
		t.Fatal("expected Push after reader injection to fail")
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	if !sc.Done() {
		t.Fatal("Close() did not close the injected reader")
	}
}

// TestUploadQueueReadDuringClose ensures a Read() blocked waiting for
// packets returns promptly once Close() runs concurrently.
func TestUploadQueueReadDuringClose(t *testing.T) {
	t.Parallel()
	q := NewUploadQueue(4)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 8)
		for {
			_, err := q.Read(buf)
			if err != nil {
				return
			}
		}
	}()
	time.Sleep(10 * time.Millisecond)
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Read() did not observe Close(): deadlock")
	}
}
