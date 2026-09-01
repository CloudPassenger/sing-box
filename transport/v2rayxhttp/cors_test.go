package xhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
)

// TestServeHTTPOptionsPreflight is a regression test for Xray-core #5720
// items 1/6 (Browser Dialer CORS): an OPTIONS preflight must get a
// same-origin-safe response derived from the request (echoed Origin,
// requested method/headers) short-circuited before any padding/session
// validation runs, so it never gets rejected as an invalid uplink/downlink
// request.
func TestServeHTTPOptionsPreflight(t *testing.T) {
	t.Parallel()
	opts := &option.V2RayXHTTPOptions{
		Mode: "packet-up",
		V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
			Path:          "/test",
			XPaddingBytes: Xbadoption.Range{From: 1, To: 100000},
		},
	}
	s := &Server{
		logger:  logger.NOP(),
		options: opts,
		path:    opts.GetNormalizedPath(),
	}

	req := httptest.NewRequest(http.MethodOptions, "/test/", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "X-Custom")

	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "POST" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, "POST")
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "X-Custom" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "X-Custom")
	}
}

// TestServeHTTPCORSCredentialsForCookiePlacement verifies
// Access-Control-Allow-Credentials is only set when session/seq/padding/
// uplink data can be placed in cookies, since combining it with a wildcard
// Access-Control-Allow-Origin is invalid per the Fetch spec.
func TestServeHTTPCORSCredentialsForCookiePlacement(t *testing.T) {
	t.Parallel()
	opts := &option.V2RayXHTTPOptions{
		Mode: "packet-up",
		V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
			Path:             "/test",
			SessionPlacement: option.PlacementCookie,
			SessionKey:       "x_session",
			XPaddingBytes:    Xbadoption.Range{From: 1, To: 100000},
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

	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
}
