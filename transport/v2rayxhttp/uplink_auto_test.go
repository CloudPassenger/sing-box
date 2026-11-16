package xhttp

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
)

// TestServeHTTPAutoUplinkPlacementConcatenates is an integration test for
// Xray-core #5720 item 5 (uplink_data_placement=auto): a single inbound
// must accept uplink data arriving split across header, cookie, and body
// simultaneously and concatenate it in that order, without depending on the
// removed *-Length marker header (Xray-core #5720 item 4).
func TestServeHTTPAutoUplinkPlacementConcatenates(t *testing.T) {
	t.Parallel()
	opts := &option.V2RayXHTTPOptions{
		Mode: "packet-up",
		V2RayXHTTPBaseOptions: option.V2RayXHTTPBaseOptions{
			Path:                "/test",
			UplinkDataPlacement: option.PlacementAuto,
			UplinkDataKey:       "X-Data",
			XPaddingBytes:       Xbadoption.Range{From: 1, To: 100000},
			ScMaxEachPostBytes:  Xbadoption.Range{From: 1000000, To: 1000000},
		},
	}
	s := &Server{
		logger:  logger.NOP(),
		options: opts,
		path:    opts.GetNormalizedPath(),
	}

	const sessionID = "sess123"
	session := s.upsertSession(sessionID)

	headerPart := []byte("AAA")
	cookiePart := []byte("BBB")
	bodyPart := []byte("CCC")

	req := httptest.NewRequest(http.MethodPost, "/test/"+sessionID+"/0?x_padding=0123456789", strings.NewReader(string(bodyPart)))
	req.Header.Set("X-Data-0", base64.RawURLEncoding.EncodeToString(headerPart))
	req.AddCookie(&http.Cookie{Name: "X-Data_0", Value: base64.RawURLEncoding.EncodeToString(cookiePart)})

	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
	}

	buf := make([]byte, 32)
	n, err := session.uploadQueue.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	want := string(headerPart) + string(cookiePart) + string(bodyPart)
	if got := string(buf[:n]); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}
