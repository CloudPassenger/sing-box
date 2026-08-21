package xhttp

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
)

type capturingRoundTripper struct {
	lastReq *http.Request
}

func (rt *capturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.lastReq = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
	}, nil
}

// TestPostPacketHeaderChunkingUsesRandomizedSize is a regression test: the
// header-placement uplink chunk size must be drawn from
// GetNormalizedUplinkChunkSize()'s range per chunk (matching Xray-core's
// anti-fingerprinting default) rather than a single fixed size repeated for
// every chunk. It also verifies the chunks reassemble to the exact original
// payload.
func TestPostPacketHeaderChunkingUsesRandomizedSize(t *testing.T) {
	t.Parallel()
	rt := &capturingRoundTripper{}
	c := &DefaultDialerClient{
		options: &option.V2RayXHTTPBaseOptions{
			UplinkDataPlacement: option.PlacementHeader,
			UplinkDataKey:       "X-Data",
			// tight, easily distinguishable range so both bounds are
			// observed across chunks with high probability.
			UplinkChunkSize: Xbadoption.Range{From: 64, To: 96},
		},
		client:      &http.Client{Transport: rt},
		httpVersion: "2",
	}

	payload := strings.Repeat("A", 5000)
	err := c.PostPacket(context.Background(), "http://example.com/", "session", "0", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if rt.lastReq == nil {
		t.Fatal("RoundTrip was never called")
	}

	var chunks []string
	sizes := map[int]bool{}
	for i := 0; ; i++ {
		v := rt.lastReq.Header.Get("X-Data-" + strconv.Itoa(i))
		if v == "" {
			break
		}
		chunks = append(chunks, v)
		sizes[len(v)] = true
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple header chunks, got %d", len(chunks))
	}
	if len(sizes) < 2 {
		t.Errorf("all %d chunks had the same length; expected randomized sizes within {64..96}, got sizes=%v", len(chunks), sizes)
	}

	encoded := strings.Join(chunks, "")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != payload {
		t.Fatalf("reassembled payload mismatch: got %d bytes, want %d bytes", len(decoded), len(payload))
	}
}
