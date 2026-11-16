package xhttp

import (
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/common/xray/net"
	"github.com/sagernet/sing-box/common/xray/signal/done"
	"github.com/sagernet/sing-box/common/xray/uuid"
	"github.com/sagernet/sing-box/option"
)

type httpSession struct {
	uploadQueue *uploadQueue
	// for as long as the GET request is not opened by the client, this will be
	// open ("undone"), and the session may be expired within a certain TTL.
	// after the client connects, this becomes "done" and the session lives as
	// long as the GET request.
	isFullyConnected *done.Instance
}

// GenerateSessionID produces a session identifier using the configured
// character table and length range when set, or a random UUID otherwise.
// A custom table/length lets a client avoid the UUID's fixed "-"-separated
// shape, which some CDNs/WAFs specifically pattern-match on and block
// (matches Xray-core #6258).
func GenerateSessionID(options *option.V2RayXHTTPBaseOptions) string {
	length := options.SessionIDLength.Rand()
	table := options.SessionIDTable
	if predefined, ok := option.PredefinedSessionIDTables[table]; ok {
		table = predefined
	}
	if table != "" && length > 0 {
		id := make([]byte, length)
		for i := range id {
			id[i] = table[rand.N(len(table))]
		}
		return string(id)
	}
	sessionIdUUID := uuid.New()
	return sessionIdUUID.String()
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

// writeCORSResponseHeader sets CORS headers for the browser dialer.
// Access-Control-Allow-Origin echoes the request's Origin (rather than a
// blanket "*") whenever session/seq/padding/uplink data can be placed in
// cookies, since a wildcard origin is invalid together with credentialed
// (cookie-carrying) requests. For OPTIONS preflight, it reflects the
// requested method/headers instead of a blanket "*", which some browsers
// reject for credentialed requests (matches Xray-core #5720).
func writeCORSResponseHeader(writer http.ResponseWriter, requestMethod string, requestHeader http.Header, usesCookies bool) {
	if origin := requestHeader.Get("Origin"); origin == "" {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		writer.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if usesCookies {
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if requestMethod == http.MethodOptions {
		if requestedMethod := requestHeader.Get("Access-Control-Request-Method"); requestedMethod != "" {
			writer.Header().Set("Access-Control-Allow-Methods", requestedMethod)
		} else {
			writer.Header().Set("Access-Control-Allow-Methods", "*")
		}
		if requestedHeaders := requestHeader.Get("Access-Control-Request-Headers"); requestedHeaders != "" {
			writer.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
		} else {
			writer.Header().Set("Access-Control-Allow-Headers", "*")
		}
	}
}

// streamUpKeepaliveEnabled decides whether the stream-up response-body
// keepalive goroutine may start. Referer carrying x_padding is the legacy
// compat marker (the default, pre-obfs-mode client always placed padding
// there). With XPaddingObfsMode, valid padding may be placed anywhere
// (header/cookie/query) instead, so a request can pass padding validation
// yet carry no Referer at all; treat that accepted obfs padding as an
// equally valid compat marker (matches Xray-core #6343). Requests with
// invalid/missing padding never reach here (rejected earlier), and
// scStreamUpServerSecs<=0 disabling keepalive is handled by the caller.
func streamUpKeepaliveEnabled(request *http.Request, obfsMode bool, obfsPaddingAccepted bool) bool {
	hasLegacyRefererCompatMarker := request.Header.Get("Referer") != ""
	return hasLegacyRefererCompatMarker || (obfsMode && obfsPaddingAccepted)
}
