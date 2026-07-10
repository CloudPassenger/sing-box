package xhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStreamUpKeepaliveEnabled is a regression test for Xray-core #6343:
// scStreamUpServerSecs keepalive must not depend solely on a legacy Referer
// header. When x_padding_obfs_mode is enabled and the request's padding was
// validated (placed in a header/cookie/query instead of Referer), keepalive
// must still be allowed to start.
func TestStreamUpKeepaliveEnabled(t *testing.T) {
	withReferer := httptest.NewRequest(http.MethodPost, "/", nil)
	withReferer.Header.Set("Referer", "https://example.com/?x_padding=abc")

	withoutReferer := httptest.NewRequest(http.MethodPost, "/", nil)

	tests := []struct {
		name                string
		request             *http.Request
		obfsMode            bool
		obfsPaddingAccepted bool
		want                bool
	}{
		{"legacy referer marker, non-obfs", withReferer, false, false, true},
		{"legacy referer marker, obfs mode", withReferer, true, false, true},
		{"no referer, non-obfs mode", withoutReferer, false, false, false},
		{"no referer, obfs mode, padding not accepted", withoutReferer, true, false, false},
		{"no referer, obfs mode, padding accepted", withoutReferer, true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamUpKeepaliveEnabled(tc.request, tc.obfsMode, tc.obfsPaddingAccepted); got != tc.want {
				t.Errorf("streamUpKeepaliveEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
