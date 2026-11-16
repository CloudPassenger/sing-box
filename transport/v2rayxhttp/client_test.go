package xhttp

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
)

type fakePacketDialerClient struct {
	closed atomic.Bool
}

func (f *fakePacketDialerClient) IsClosed() bool { return f.closed.Load() }

func (f *fakePacketDialerClient) OpenStream(_ context.Context, _ string, _ string, _ io.Reader, _ bool) (io.ReadCloser, net.Addr, net.Addr, error) {
	return io.NopCloser(bytes.NewReader(nil)), &net.TCPAddr{}, &net.TCPAddr{}, nil
}

func (f *fakePacketDialerClient) PostPacket(_ context.Context, _ string, _ string, _ string, _ io.Reader, _ int64) error {
	// Deliberately does not read `body`: reading/clearing the shared
	// buf.Buffer here would race against uploadWriter.Write's own Len()
	// call on the same buffer after handoff -- a pre-existing, unrelated
	// hazard in the upload pipe plumbing that this test is not about.
	return nil
}

type recordingDialerClient struct {
	mu      sync.Mutex
	lengths []int64
}

func (f *recordingDialerClient) IsClosed() bool { return false }

func (f *recordingDialerClient) OpenStream(_ context.Context, _ string, _ string, _ io.Reader, _ bool) (io.ReadCloser, net.Addr, net.Addr, error) {
	return io.NopCloser(bytes.NewReader(nil)), &net.TCPAddr{}, &net.TCPAddr{}, nil
}

func (f *recordingDialerClient) PostPacket(_ context.Context, _ string, _ string, _ string, body io.Reader, contentLength int64) error {
	n, _ := io.Copy(io.Discard, body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lengths = append(f.lengths, n)
	_ = contentLength
	return nil
}

func (f *recordingDialerClient) total() (sum int64, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.lengths {
		sum += n
	}
	return sum, len(f.lengths)
}

// TestPacketUpStrictChunking is a regression test for Xray-core #5720 item
// 2: the upload pipe's WithSizeLimit is a soft cap (a single WriteMultiBuffer
// call is let through uncapped when the pipe wasn't full yet), so
// ReadMultiBuffer can hand back more bytes than sc_max_each_post_bytes
// allows. Every individual PostPacket body must still be split down to at
// most maxUploadSize bytes before being sent, or a server enforcing
// scMaxEachPostBytes strictly (e.g. behind nginx's small header/body
// buffers) would reject the oversized POST.
func TestPacketUpStrictChunking(t *testing.T) {
	t.Parallel()
	const maxUploadSize = 64
	const payloadSize = 500 // not a multiple of maxUploadSize on purpose

	recorder := &recordingDialerClient{}
	xmuxUp := &XmuxClient{}
	xmuxUp.LeftRequests.Store(1 << 20)
	getHTTPClient := func() (DialerClient, *XmuxClient) { return recorder, xmuxUp }

	downClient := &fakePacketDialerClient{}
	xmuxDown := &XmuxClient{}
	xmuxDown.LeftRequests.Store(1 << 20)
	getHTTPClient2 := func() (DialerClient, *XmuxClient) { return downClient, xmuxDown }

	c := &Client{
		options: &option.V2RayXHTTPOptions{
			Mode: "packet-up",
			V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
				ScMaxEachPostBytes:   Xbadoption.Range{From: maxUploadSize, To: maxUploadSize},
				ScMinPostsIntervalMs: Xbadoption.Range{From: 0, To: 1},
			},
		},
		baseRequestURL:  url.URL{Scheme: "http", Host: "example.com", Path: "/"},
		baseRequestURL2: url.URL{Scheme: "http", Host: "example.com", Path: "/"},
		getHTTPClient:   getHTTPClient,
		getHTTPClient2:  getHTTPClient2,
	}

	conn, err := c.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{'a'}, payloadSize)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var sum int64
	var count int
	for time.Now().Before(deadline) {
		sum, count = recorder.total()
		if sum >= payloadSize {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if sum != payloadSize {
		t.Fatalf("total bytes posted = %d, want %d", sum, payloadSize)
	}
	if count < payloadSize/maxUploadSize {
		t.Fatalf("expected at least %d PostPacket calls, got %d", payloadSize/maxUploadSize, count)
	}
	recorder.mu.Lock()
	for i, n := range recorder.lengths {
		if n > maxUploadSize {
			recorder.mu.Unlock()
			t.Fatalf("PostPacket call %d carried %d bytes, want <= %d (sc_max_each_post_bytes)", i, n, maxUploadSize)
		}
	}
	recorder.mu.Unlock()
}

// TestPacketUpRotationNeverTouchesOuterXmuxClient is a regression test for
// the accounting bug discussed in Xray-core #6140: the packet-up upload
// goroutine may rotate to a different xmux client mid-stream (when
// LeftRequests is exhausted or UnreusableAt elapses), but it must do so
// through a variable LOCAL to that goroutine. The outer xmuxClient/
// httpClient captured by onClose() (read when the connection closes) must
// never be reassigned by the rotation, or onClose() would release whichever
// client the loop last rotated to instead of the client actually retained
// (AddRunning'd) at dial time, leaking the original client's running count
// and corrupting the rotated-to client's count.
func TestPacketUpRotationNeverTouchesOuterXmuxClient(t *testing.T) {
	t.Parallel()
	clientA := &fakePacketDialerClient{}
	clientB := &fakePacketDialerClient{}
	xmuxA := &XmuxClient{XmuxConn: clientA}
	xmuxA.LeftRequests.Store(1) // the next Add(-1) hits 0, forcing rotation
	xmuxB := &XmuxClient{XmuxConn: clientB}
	xmuxB.LeftRequests.Store(1 << 20)

	var primaryCalls atomic.Int32
	getHTTPClient := func() (DialerClient, *XmuxClient) {
		if primaryCalls.Add(1) == 1 {
			return clientA, xmuxA
		}
		return clientB, xmuxB
	}

	downClient := &fakePacketDialerClient{}
	xmuxDown := &XmuxClient{XmuxConn: downClient}
	xmuxDown.LeftRequests.Store(1 << 20)
	getHTTPClient2 := func() (DialerClient, *XmuxClient) {
		return downClient, xmuxDown
	}

	c := &Client{
		options: &option.V2RayXHTTPOptions{
			Mode: "packet-up",
		},
		baseRequestURL:  url.URL{Scheme: "http", Host: "example.com", Path: "/"},
		baseRequestURL2: url.URL{Scheme: "http", Host: "example.com", Path: "/"},
		getHTTPClient:   getHTTPClient,
		getHTTPClient2:  getHTTPClient2,
	}

	conn, err := c.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// AddRunning() at dial time must land on the client actually retained
	// (xmuxA for uplink, xmuxDown for downlink) -- not on xmuxB, which is
	// only reachable through mid-loop rotation.
	if got := xmuxA.Running.Load(); got != 1 {
		t.Fatalf("xmuxA.Running after dial = %d, want 1", got)
	}
	if got := xmuxDown.Running.Load(); got != 1 {
		t.Fatalf("xmuxDown.Running after dial = %d, want 1", got)
	}
	if got := xmuxB.Running.Load(); got != 0 {
		t.Fatalf("xmuxB.Running after dial = %d, want 0 (not yet rotated to)", got)
	}

	// One write is enough to exhaust xmuxA's LeftRequests and force the
	// upload loop to rotate to xmuxB via getHTTPClient() a second time.
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	// give the background upload goroutine time to observe the write,
	// rotate, and post through clientB.
	deadline := time.Now().Add(2 * time.Second)
	for primaryCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if primaryCalls.Load() < 2 {
		t.Fatal("upload loop never rotated to a second xmux client")
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// a second Close() must be a no-op (guarded by `closed`), not a
	// double-release of the running count.
	_ = conn.Close()

	if got := xmuxA.Running.Load(); got != 0 {
		t.Fatalf("xmuxA.Running after close = %d, want 0 (leaked)", got)
	}
	if got := xmuxDown.Running.Load(); got != 0 {
		t.Fatalf("xmuxDown.Running after close = %d, want 0 (leaked)", got)
	}
	if got := xmuxB.Running.Load(); got != 0 {
		t.Fatalf("xmuxB.Running after close = %d, want 0 (rotation must never touch Running)", got)
	}
}
