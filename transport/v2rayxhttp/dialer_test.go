package xhttp

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/option"
)

// TestOpenStreamInvalidURLReturnsError guards against XTLS/Xray-core#6316:
// an invalid request URL/host must produce an error, not a nil-pointer
// panic on the discarded *http.Request.
func TestOpenStreamInvalidURLReturnsError(t *testing.T) {
	c := &DefaultDialerClient{
		options: &option.V2RayXHTTPBaseOptions{},
	}
	_, _, _, err := c.OpenStream(context.Background(), "http://example.com/%zz", "session", nil, false)
	if err == nil {
		t.Fatal("expected error for invalid request URL, got nil")
	}
}
