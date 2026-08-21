package xhttp

import (
	"strings"
	"testing"

	Xbadoption "github.com/sagernet/sing-box/common/xray/json/badoption"
	"github.com/sagernet/sing-box/option"
)

// TestGenerateSessionIDCustomTable is a regression test for Xray-core
// #6258: a configured session_id_table/session_id_length must produce IDs
// drawn only from that table at the configured length, instead of always
// falling back to a UUID (whose fixed "xxxxxxxx-xxxx-..." shape is itself a
// detectable pattern some CDNs/WAFs block on).
func TestGenerateSessionIDCustomTable(t *testing.T) {
	t.Parallel()
	opts := &option.V2RayXHTTPBaseOptions{
		SessionIDTable:  "0123456789abcdef",
		SessionIDLength: Xbadoption.Range{From: 20, To: 20},
	}
	for range 20 {
		id := GenerateSessionID(opts)
		if len(id) != 20 {
			t.Fatalf("len(id) = %d, want 20 (id=%q)", len(id), id)
		}
		if strings.ContainsAny(id, "-ghijklmnopqrstuvwxyzGHIJKLMNOPQRSTUVWXYZ") {
			t.Fatalf("id %q contains characters outside the configured table", id)
		}
	}
}

// TestGenerateSessionIDFallsBackToUUID verifies the default (no table
// configured) behavior is unchanged: a standard UUID string.
func TestGenerateSessionIDFallsBackToUUID(t *testing.T) {
	t.Parallel()
	opts := &option.V2RayXHTTPBaseOptions{}
	id := GenerateSessionID(opts)
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("id = %q, want a UUID-shaped string", id)
	}
}
