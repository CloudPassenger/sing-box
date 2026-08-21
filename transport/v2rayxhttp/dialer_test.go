package xhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

// TestOpenStreamInvalidURLReturnsError guards against XTLS/Xray-core#6316:
// an invalid request URL/host must produce an error, not a nil-pointer
// panic on the discarded *http.Request.
func TestOpenStreamInvalidURLReturnsError(t *testing.T) {
	t.Parallel()
	c := &DefaultDialerClient{
		options: &option.V2RayXHTTPBaseOptions{},
	}
	_, _, _, err := c.OpenStream(context.Background(), "http://example.com/%zz", "session", nil, false)
	if err == nil {
		t.Fatal("expected error for invalid request URL, got nil")
	}
}

type trackedCloser struct {
	*strings.Reader
	closed atomic.Bool
}

func (t *trackedCloser) Close() error {
	t.closed.Store(true)
	return nil
}

func waitUntilClosed(t *testing.T, body *trackedCloser) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if body.closed.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("body was never closed")
}

type fakeAddr struct{}

func (fakeAddr) Network() string { return "tcp" }
func (fakeAddr) String() string  { return "127.0.0.1:0" }

// fakeConn implements just enough of net.Conn for httptrace.GotConnInfo's
// consumer in OpenStream (which only calls RemoteAddr/LocalAddr).
type fakeConn struct{ net.Conn }

func (fakeConn) RemoteAddr() net.Addr { return fakeAddr{} }
func (fakeConn) LocalAddr() net.Addr  { return fakeAddr{} }

// nonClosingRoundTripper deliberately violates the http.RoundTripper
// contract by never touching req.Body, simulating a third-party
// RoundTripper (e.g. golang.org/x/net/http2.Transport / http3.Transport)
// that does not reliably close the request body on every path. This
// isolates OpenStream's OWN defensive common.Close(body) call from
// whatever the stdlib net/http.Transport might already guarantee.
// It still fires the httptrace GotConn hook OpenStream installs (with a
// fake connection), since real RoundTrippers do this as part of dialing
// and OpenStream blocks on it.
type nonClosingRoundTripper struct {
	resp *http.Response
	err  error
}

func (rt *nonClosingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Conn: fakeConn{}})
	}
	if rt.err != nil {
		return nil, rt.err
	}
	return rt.resp, nil
}

// TestOpenStreamClosesBodyOnTransportError is a regression test for
// Xray-core #6095: when client.Do returns an error, OpenStream must
// explicitly close the caller-owned body (the pipe.Reader feeding
// conn.Write for stream-up/one) itself, rather than assuming the
// RoundTripper already did. Leaving it open means the outbound believes
// the upload pipe is still writable and hangs indefinitely.
func TestOpenStreamClosesBodyOnTransportError(t *testing.T) {
	t.Parallel()
	c := &DefaultDialerClient{
		options: &option.V2RayXHTTPBaseOptions{},
		client: &http.Client{
			Transport: &nonClosingRoundTripper{err: errors.New("boom")},
		},
	}
	body := &trackedCloser{Reader: strings.NewReader("payload")}
	_, _, _, _ = c.OpenStream(context.Background(), "http://example.com/", "session", body, true)
	waitUntilClosed(t, body)
}

// TestOpenStreamClosesBodyAfterUploadOnlyResponse covers the ordinary
// (successful) stream-up/one completion path: once uploadOnly's single
// response is consumed, OpenStream must explicitly close body itself
// (matches Xray-core #6095), independent of whether the RoundTripper also
// does so.
func TestOpenStreamClosesBodyAfterUploadOnlyResponse(t *testing.T) {
	t.Parallel()
	c := &DefaultDialerClient{
		options: &option.V2RayXHTTPBaseOptions{},
		client: &http.Client{
			Transport: &nonClosingRoundTripper{resp: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
			}},
		},
	}
	body := &trackedCloser{Reader: strings.NewReader("payload")}
	_, _, _, err := c.OpenStream(context.Background(), "http://example.com/", "session", body, true)
	if err != nil {
		t.Fatal(err)
	}
	waitUntilClosed(t, body)
}
