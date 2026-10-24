// Package safehttp provides an outbound HTTP client for URLs that originate in
// untrusted Discord content (message embeds, link-preview images). It enforces
// the SSRF protections the bot's image filter already uses: HTTPS on port 443,
// no redirects, and a dial-time guard that refuses every non-public address —
// including the reserved ranges the stdlib predicates miss.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedURL reports a URL or address refused by the outbound policy.
var ErrBlockedURL = errors.New("blocked unsafe URL")

// blockedDialRanges are non-public ranges that are neither private, loopback,
// link-local nor unspecified — the stdlib predicates miss them, so an SSRF
// target such as a CGNAT or benchmarking address would otherwise be dialable.
var blockedDialRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // RFC 6598 carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // RFC 6890 IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),  // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), // reserved (incl. 255.255.255.255)
}

// NewClient returns a client for untrusted media URLs. The request is limited
// to HTTPS on port 443 and the dialer refuses private, loopback, link-local,
// multicast and reserved targets after DNS resolution. Redirects are refused so
// an allowed host cannot bounce the request to another address.
func NewClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: dialGuard}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: policyTransport{inner: tr},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("%w: redirects are not allowed", ErrBlockedURL)
		},
	}
}

// policyTransport rejects non-HTTPS requests and non-standard ports before
// they reach the network.
type policyTransport struct {
	inner http.RoundTripper
}

func (t policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%w: only HTTPS URLs are fetched", ErrBlockedURL)
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("%w: only port 443 is allowed", ErrBlockedURL)
	}
	return t.inner.RoundTrip(req)
}

// dialGuard refuses connections to anything that is not a public unicast
// address. It runs in the dialer's Control hook, i.e. on the resolved address,
// so a hostile DNS answer cannot smuggle a private target past the hostname.
func dialGuard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refusing to dial malformed address %q: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("refusing to dial unparsable address %q", host)
	}
	ip = ip.Unmap()
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("refusing to dial non-public address %s", ip)
	}
	for _, p := range blockedDialRanges {
		if p.Contains(ip) {
			return fmt.Errorf("refusing to dial reserved address %s", ip)
		}
	}
	return nil
}
