package xhttp

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/sagernet/sing-box/option"
)

// TestExtractMetaFromRequestMixedPlacement is a regression test for the
// server-side counterpart of Xray-core #5720 item 7 ("allow the only one of
// the sessionPlacement or the seqPlacement to be path"): the previous
// implementation only handled the two placements being either both "path"
// or both non-path, silently dropping session/seq values whenever exactly
// one of them lived in the path and the other in a header/cookie/query.
func TestExtractMetaFromRequestMixedPlacement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		sessionInPath bool
		seqInPath     bool
		sessionKey    string
		seqKey        string
		path          string
		requestPath   string
		requestHeader http.Header
		wantSessionID string
		wantSeq       string
	}{
		{
			name:          "session in path, seq in header",
			sessionInPath: true,
			seqKey:        "X-Seq",
			path:          "/base/",
			requestPath:   "/base/abc123",
			requestHeader: http.Header{"X-Seq": []string{"42"}},
			wantSessionID: "abc123",
			wantSeq:       "42",
		},
		{
			name:          "seq in path, session in header",
			seqInPath:     true,
			sessionKey:    "X-Session",
			path:          "/base/",
			requestPath:   "/base/7",
			requestHeader: http.Header{"X-Session": []string{"sess-xyz"}},
			wantSessionID: "sess-xyz",
			wantSeq:       "7",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := &option.V2RayXHTTPOptions{}
			opts.SessionKey = tc.sessionKey
			opts.SeqKey = tc.seqKey
			if !tc.sessionInPath {
				opts.SessionPlacement = option.PlacementHeader
			}
			if !tc.seqInPath {
				opts.SeqPlacement = option.PlacementHeader
			}
			req := &http.Request{
				URL:    &url.URL{Path: tc.requestPath},
				Header: tc.requestHeader,
			}
			if req.Header == nil {
				req.Header = http.Header{}
			}
			sessionID, seq := ExtractMetaFromRequest(opts, req, tc.path)
			if sessionID != tc.wantSessionID {
				t.Errorf("sessionID = %q, want %q", sessionID, tc.wantSessionID)
			}
			if seq != tc.wantSeq {
				t.Errorf("seq = %q, want %q", seq, tc.wantSeq)
			}
		})
	}
}
