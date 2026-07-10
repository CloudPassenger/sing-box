package xhttp

import (
	"context"
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
)

type fakeXmuxConn struct {
	closed bool
}

func (f *fakeXmuxConn) IsClosed() bool { return f.closed }
func (f *fakeXmuxConn) Close() error {
	f.closed = true
	return nil
}

func TestMaxConnections(t *testing.T) {
	xmuxOptions := option.V2RayXHTTPXmuxOptions{
		MaxConnections: badoptionRange(4, 4),
	}
	m := NewXmuxManager(xmuxOptions, func() XmuxConn { return &fakeXmuxConn{} })
	seen := make(map[*XmuxClient]struct{})
	for i := 0; i < 8; i++ {
		seen[m.GetXmuxClient(context.Background())] = struct{}{}
	}
	if len(seen) != 4 {
		t.Fatalf("did not get 4 distinct clients, got %d", len(seen))
	}
}

func TestMaxConcurrency(t *testing.T) {
	xmuxOptions := option.V2RayXHTTPXmuxOptions{
		MaxConcurrency: badoptionRange(2, 2),
	}
	m := NewXmuxManager(xmuxOptions, func() XmuxConn { return &fakeXmuxConn{} })
	seen := make(map[*XmuxClient]struct{})
	for i := 0; i < 64; i++ {
		c := m.GetXmuxClient(context.Background())
		c.AddRunning()
		seen[c] = struct{}{}
	}
	if len(seen) != 32 {
		t.Fatalf("did not get 32 distinct clients, got %d", len(seen))
	}
}

// TestEvictionDoesNotCloseActiveStream is a regression test: when
// GetXmuxClient evicts a client from the pool (e.g. because
// HMaxReusableSecs elapsed or HMaxRequestTimes was hit) while it still has
// running requests (AddRunning), the underlying connection must stay open
// until every running request finishes (DoneRunning). Closing it
// immediately on eviction would cut off in-flight streams.
func TestEvictionDoesNotCloseActiveStream(t *testing.T) {
	conn := &fakeXmuxConn{}
	m := NewXmuxManager(option.V2RayXHTTPXmuxOptions{
		MaxConnections: badoptionRange(1, 1),
	}, func() XmuxConn { return conn })

	client := m.GetXmuxClient(context.Background())
	client.AddRunning() // simulate an active stream on this client

	// force eviction on the next GetXmuxClient call.
	client.LeftRequests.Store(0)
	_ = m.GetXmuxClient(context.Background())

	if conn.closed {
		t.Fatal("eviction closed the connection while a request was still running")
	}

	// once the running request finishes, the connection must be closed.
	client.DoneRunning()
	if !conn.closed {
		t.Fatal("connection was not closed after the last running request finished")
	}
}

// TestEvictionClosesIdleClientImmediately: a client with no running
// requests at eviction time closes right away.
func TestEvictionClosesIdleClientImmediately(t *testing.T) {
	conn := &fakeXmuxConn{}
	m := NewXmuxManager(option.V2RayXHTTPXmuxOptions{
		MaxConnections: badoptionRange(1, 1),
	}, func() XmuxConn { return conn })

	client := m.GetXmuxClient(context.Background())
	client.LeftRequests.Store(0)
	_ = m.GetXmuxClient(context.Background())

	if !conn.closed {
		t.Fatal("idle client was not closed immediately on eviction")
	}
}

func badoptionRange(from, to int32) Xbadoption.Range {
	return Xbadoption.Range{From: from, To: to}
}
