package option

import (
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
)

// TestXHTTPGetNormalizedPathConditionalSlash is a regression test for
// Xray-core #6307: GetNormalizedPath previously always appended a trailing
// "/", which is only meaningful when session/seq is placed in the path (it
// separates the configured base path from the appended segments). Forcing
// it unconditionally turned clean "file-like" paths (e.g. "/stream/x.ext")
// into "/stream/x.ext/", which some CDNs/WAFs treat as suspicious or 403.
func TestXHTTPGetNormalizedPathConditionalSlash(t *testing.T) {
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
