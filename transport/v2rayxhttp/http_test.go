package xhttp

import (
	"net/http"
	"testing"
)

// TestTrustedForwardedAddrsNoXFF: no X-Forwarded-For header at all -> no
// addresses, no warning, nothing forged.
func TestTrustedForwardedAddrsNoXFF(t *testing.T) {
	h := http.Header{}
	addrs, warn, forged := trustedForwardedAddrs(h, nil)
	if addrs != nil || warn || forged {
		t.Fatalf("unexpected result: addrs=%v warn=%v forged=%v", addrs, warn, forged)
	}
}

// TestTrustedForwardedAddrsUntrustedByDefault reproduces the gap versus
// XTLS/Xray-core#6309: without any configured trusted marker, an incoming
// X-Forwarded-For must NOT be trusted (no addrs), and must be surfaced as a
// warning rather than silently accepted.
func TestTrustedForwardedAddrsUntrustedByDefault(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-For", "203.0.113.9")
	addrs, warn, forged := trustedForwardedAddrs(h, nil)
	if addrs != nil {
		t.Fatalf("expected X-Forwarded-For to be ignored without trusted markers, got %v", addrs)
	}
	if !warn {
		t.Fatal("expected warn=true when trusted_x_forwarded_for is not configured")
	}
	if forged {
		t.Fatal("did not expect forged=true when no trusted markers are configured")
	}
}

// TestTrustedForwardedAddrsMissingMarkerIsForged: trusted markers are
// configured, but this request doesn't carry any of them alongside
// X-Forwarded-For -> treat as a potential forgery, do not trust the value.
func TestTrustedForwardedAddrsMissingMarkerIsForged(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-For", "203.0.113.9")
	addrs, warn, forged := trustedForwardedAddrs(h, []string{"X-Trusted-CDN"})
	if addrs != nil {
		t.Fatalf("expected X-Forwarded-For to be ignored without the trusted marker, got %v", addrs)
	}
	if warn {
		t.Fatal("did not expect warn=true when trusted markers are configured")
	}
	if !forged {
		t.Fatal("expected forged=true when the trusted marker is missing")
	}
}

// TestTrustedForwardedAddrsTrusted: the trusted marker is present -> the
// forwarded address is honored.
func TestTrustedForwardedAddrsTrusted(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-For", "203.0.113.9")
	h.Set("X-Trusted-CDN", "1")
	addrs, warn, forged := trustedForwardedAddrs(h, []string{"X-Trusted-CDN"})
	if warn || forged {
		t.Fatalf("unexpected warn=%v forged=%v", warn, forged)
	}
	if len(addrs) != 1 || addrs[0].String() != "203.0.113.9" {
		t.Fatalf("unexpected addrs: %v", addrs)
	}
}
