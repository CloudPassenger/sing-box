package xhttp

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

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
