package xhttp

import (
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/common/xray/net"
	"github.com/sagernet/sing-box/common/xray/signal/done"
)

type httpSession struct {
	uploadQueue *uploadQueue
	// for as long as the GET request is not opened by the client, this will be
	// open ("undone"), and the session may be expired within a certain TTL.
	// after the client connects, this becomes "done" and the session lives as
	// long as the GET request.
	isFullyConnected *done.Instance
}

func parseXForwardedFor(header http.Header) []net.Address {
	xff := header.Get("X-Forwarded-For")
	if xff == "" {
		return nil
	}
	list := strings.Split(xff, ",")
	addrs := make([]net.Address, 0, len(list))
	for _, proxy := range list {
		addrs = append(addrs, net.ParseAddress(proxy))
	}
	return addrs
}

// trustedForwardedAddrs returns the addresses parsed from X-Forwarded-For
// only when the request carries at least one of the configured trusted
// marker headers. It never trusts X-Forwarded-For by default: if no marker
// is configured, warn is true (once) instead of silently trusting the
// remote-supplied header; if markers are configured but absent, forged is
// true, signaling a potential spoofing attempt.
func trustedForwardedAddrs(header http.Header, trusted []string) (addrs []net.Address, warn bool, forged bool) {
	if header.Get("X-Forwarded-For") == "" {
		return nil, false, false
	}
	for _, key := range trusted {
		if len(header.Values(key)) > 0 {
			return parseXForwardedFor(header), false, false
		}
	}
	if len(trusted) == 0 {
		return nil, true, false
	}
	return nil, false, true
}

func isValidHTTPHost(request string, config string) bool {
	r := strings.ToLower(request)
	c := strings.ToLower(config)
	if strings.Contains(r, ":") {
		h, _, _ := net.SplitHostPort(r)
		return h == c
	}
	return r == c
}
