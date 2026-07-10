package xhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
)

// TestServeHTTPCookiePaddingIsSet is a regression test for Xray-core #5720
// item 6: with x_padding_obfs_mode enabled and x_padding_placement=cookie,
// ApplyXPaddingToHeader alone (a bare http.Header) cannot express cookie
// placement, so the server never actually set the padding cookie on its
// response. The response must carry a Set-Cookie for the configured
// padding key.
func TestServeHTTPCookiePaddingIsSet(t *testing.T) {
	opts := &option.V2RayXHTTPOptions{
		Mode: "packet-up",
		V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
			Path:              "/test",
			XPaddingBytes:     Xbadoption.Range{From: 10, To: 100},
			XPaddingObfsMode:  true,
			XPaddingPlacement: option.PlacementCookie,
			XPaddingKey:       "x_pad",
		},
	}
	s := &Server{
		logger:  logger.NOP(),
		options: opts,
		path:    opts.GetNormalizedPath(),
	}

	req := httptest.NewRequest(http.MethodOptions, "/test/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	resp := w.Result()
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "x_pad" && c.Value != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a Set-Cookie for %q, got cookies: %v", "x_pad", resp.Cookies())
	}
}
