package option

import (
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

// TestXHTTPGetNormalizedPathConditionalSlash is a regression test for
// Xray-core #6307: GetNormalizedPath previously always appended a trailing
// "/", which is only meaningful when session/seq is placed in the path (it
// separates the configured base path from the appended segments). Forcing
// it unconditionally turned clean "file-like" paths (e.g. "/stream/x.ext")
// into "/stream/x.ext/", which some CDNs/WAFs treat as suspicious or 403.
func TestXHTTPGetNormalizedPathConditionalSlash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		path             string
		sessionPlacement string
		seqPlacement     string
		want             string
	}{
		{
			name: "default placement keeps trailing slash",
			path: "/sh",
			want: "/sh/",
		},
		{
			name:             "both off path drops trailing slash",
			path:             "/stream",
			sessionPlacement: PlacementQuery,
			seqPlacement:     PlacementQuery,
			want:             "/stream",
		},
		{
			name:             "both off path keeps file-like path clean",
			path:             "/stream/filename.extension",
			sessionPlacement: PlacementQuery,
			seqPlacement:     PlacementHeader,
			want:             "/stream/filename.extension",
		},
		{
			name:             "seq in path keeps trailing slash",
			path:             "/stream",
			sessionPlacement: PlacementQuery,
			want:             "/stream/",
		},
		{
			name:         "session in path keeps trailing slash",
			path:         "/stream",
			seqPlacement: PlacementCookie,
			want:         "/stream/",
		},
		{
			name:             "existing trailing slash preserved",
			path:             "/stream/",
			sessionPlacement: PlacementQuery,
			seqPlacement:     PlacementQuery,
			want:             "/stream/",
		},
		{
			name:             "root unchanged",
			path:             "/",
			sessionPlacement: PlacementQuery,
			seqPlacement:     PlacementQuery,
			want:             "/",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &V2RayXHTTPBaseOptions{
				Path:             tc.path,
				SessionPlacement: tc.sessionPlacement,
				SeqPlacement:     tc.seqPlacement,
			}
			if got := c.GetNormalizedPath(); got != tc.want {
				t.Errorf("GetNormalizedPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestXHTTPDefaultXmuxMaxConnections is a regression test for Xray-core
// #6376 (anti-RKN default): when xmux is not configured at all, the
// default should be maxConnections=6 rather than maxConcurrency=1. A
// single reused connection is more identifiable to DPI/censorship
// heuristics that specifically target XHTTP than several distinct
// connections would be.
func TestXHTTPDefaultXmuxMaxConnections(t *testing.T) {
	t.Parallel()
	opts := &V2RayXHTTPOptions{Mode: "packet-up"}
	opts.XPaddingBytes = Xbadoption.Range{From: 100, To: 1000}
	if err := checkV2RayXHTTPBaseOptions(opts.Mode, &opts.V2RayXHTTPBaseOptions); err != nil {
		t.Fatal(err)
	}
	if opts.Xmux == nil {
		t.Fatal("expected default xmux to be populated")
	}
	if opts.Xmux.MaxConnections.From != 6 || opts.Xmux.MaxConnections.To != 6 {
		t.Errorf("default MaxConnections = %+v, want {6 6}", opts.Xmux.MaxConnections)
	}
	if opts.Xmux.MaxConcurrency.From != 0 || opts.Xmux.MaxConcurrency.To != 0 {
		t.Errorf("default MaxConcurrency = %+v, want zero value", opts.Xmux.MaxConcurrency)
	}
}

// TestXHTTPServerMaxHeaderBytesValidation checks the server_max_header_bytes
// validation and default (matches Xray-core's serverMaxHeaderBytes).
func TestXHTTPServerMaxHeaderBytesValidation(t *testing.T) {
	t.Parallel()
	base := func() *V2RayXHTTPBaseOptions {
		return &V2RayXHTTPBaseOptions{XPaddingBytes: Xbadoption.Range{From: 100, To: 1000}}
	}

	def := base()
	if err := checkV2RayXHTTPBaseOptions("packet-up", def); err != nil {
		t.Fatal(err)
	}
	if got := def.GetNormalizedServerMaxHeaderBytes(); got != 8192 {
		t.Errorf("default GetNormalizedServerMaxHeaderBytes() = %d, want 8192", got)
	}

	custom := base()
	custom.ServerMaxHeaderBytes = 16384
	if err := checkV2RayXHTTPBaseOptions("packet-up", custom); err != nil {
		t.Fatal(err)
	}
	if got := custom.GetNormalizedServerMaxHeaderBytes(); got != 16384 {
		t.Errorf("GetNormalizedServerMaxHeaderBytes() = %d, want 16384", got)
	}

	negative := base()
	negative.ServerMaxHeaderBytes = -1
	if err := checkV2RayXHTTPBaseOptions("packet-up", negative); err == nil {
		t.Fatal("expected error for negative server_max_header_bytes")
	}
}

// TestXHTTPSessionIDTableValidation is a regression test for Xray-core
// #6258: session_id_table must be ASCII-only, session_id_length.from must
// be positive, and the table/length combination must offer a large enough
// ID space (>= 2^31) to keep collisions and brute-forcing impractical.
func TestXHTTPSessionIDTableValidation(t *testing.T) {
	t.Parallel()
	base := func() *V2RayXHTTPBaseOptions {
		return &V2RayXHTTPBaseOptions{XPaddingBytes: Xbadoption.Range{From: 100, To: 1000}}
	}

	t.Run("predefined table name expands and normalizes", func(t *testing.T) {
		t.Parallel()
		c := base()
		c.SessionIDTable = "hex"
		c.SessionIDLength = Xbadoption.Range{From: 32, To: 32}
		if err := checkV2RayXHTTPBaseOptions("packet-up", c); err != nil {
			t.Fatal(err)
		}
		if c.SessionIDTable != "0123456789abcdef" {
			t.Errorf("SessionIDTable = %q, want expanded predefined table", c.SessionIDTable)
		}
	})

	t.Run("non-ASCII table rejected", func(t *testing.T) {
		t.Parallel()
		c := base()
		c.SessionIDTable = "abcé"
		c.SessionIDLength = Xbadoption.Range{From: 32, To: 32}
		if err := checkV2RayXHTTPBaseOptions("packet-up", c); err == nil {
			t.Fatal("expected error for non-ASCII session_id_table")
		}
	})

	t.Run("zero length rejected", func(t *testing.T) {
		t.Parallel()
		c := base()
		c.SessionIDTable = "0123456789abcdef"
		c.SessionIDLength = Xbadoption.Range{From: 0, To: 0}
		if err := checkV2RayXHTTPBaseOptions("packet-up", c); err == nil {
			t.Fatal("expected error for session_id_length.from == 0")
		}
	})

	t.Run("too small ID space rejected", func(t *testing.T) {
		t.Parallel()
		c := base()
		c.SessionIDTable = "01" // 2-symbol table
		c.SessionIDLength = Xbadoption.Range{From: 1, To: 1}
		if err := checkV2RayXHTTPBaseOptions("packet-up", c); err == nil {
			t.Fatal("expected error for a session ID space that is too small")
		}
	})
}

// TestXHTTPGetNormalizedUplinkChunkSize is a regression test: the default
// chunk size for header/cookie uplink placement must be a randomized
// range (matching Xray-core's anti-fingerprinting default), not one fixed
// size, and a plain JSON number must still parse into a fixed range for
// backward compatibility with existing "uplink_chunk_size": <n> configs.
func TestXHTTPGetNormalizedUplinkChunkSize(t *testing.T) {
	t.Parallel()
	t.Run("cookie default is a range", func(t *testing.T) {
		t.Parallel()
		c := &V2RayXHTTPBaseOptions{UplinkDataPlacement: PlacementCookie}
		got := c.GetNormalizedUplinkChunkSize()
		if got.From != 2*1024 || got.To != 3*1024 {
			t.Errorf("default cookie chunk size = %+v, want {2048 3072}", got)
		}
	})

	t.Run("header default is a range", func(t *testing.T) {
		t.Parallel()
		c := &V2RayXHTTPBaseOptions{UplinkDataPlacement: PlacementHeader}
		got := c.GetNormalizedUplinkChunkSize()
		if got.From != 3000 || got.To != 4000 {
			t.Errorf("default header chunk size = %+v, want {3000 4000}", got)
		}
	})

	t.Run("explicit value below 64 is clamped", func(t *testing.T) {
		t.Parallel()
		c := &V2RayXHTTPBaseOptions{
			UplinkDataPlacement: PlacementHeader,
			UplinkChunkSize:     Xbadoption.Range{From: 10, To: 10},
		}
		got := c.GetNormalizedUplinkChunkSize()
		if got.From != 64 || got.To != 64 {
			t.Errorf("clamped chunk size = %+v, want {64 64}", got)
		}
	})

	t.Run("plain JSON number parses as a fixed range", func(t *testing.T) {
		t.Parallel()
		var r Xbadoption.Range
		if err := r.UnmarshalJSON([]byte("4096")); err != nil {
			t.Fatal(err)
		}
		c := &V2RayXHTTPBaseOptions{UplinkDataPlacement: PlacementHeader, UplinkChunkSize: r}
		got := c.GetNormalizedUplinkChunkSize()
		if got.From != 4096 || got.To != 4096 {
			t.Errorf("fixed chunk size = %+v, want {4096 4096}", got)
		}
	})
}

func TestV2RayWebSocketRequestHostJSONRoundTrip(t *testing.T) {
	t.Parallel()

	transport := V2RayTransportOptions{
		Type: C.V2RayTransportTypeWebsocket,
		WebsocketOptions: V2RayWebsocketOptions{
			RequestHost: "front.example",
			Path:        "/ws",
		},
	}
	content, err := json.Marshal(transport)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"ws","request_host":"front.example","path":"/ws"}`, string(content))

	var decoded V2RayTransportOptions
	require.NoError(t, json.Unmarshal(content, &decoded))
	require.Equal(t, C.V2RayTransportTypeWebsocket, decoded.Type)
	require.Equal(t, "front.example", decoded.WebsocketOptions.RequestHost)
	require.Equal(t, "/ws", decoded.WebsocketOptions.Path)

	transport.WebsocketOptions.RequestHost = ""
	content, err = json.Marshal(transport)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"ws","path":"/ws"}`, string(content))
}
