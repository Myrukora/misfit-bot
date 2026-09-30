package imagefilter

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// fetch safeguards, ported exactly from the Python module:
// HTTPS only, Discord CDN hosts only, redirects refused (a redirect off the
// CDN could otherwise bypass the allowlist), 10s timeout, 50 MiB read cap.

var errBlockedURL = errors.New("rejected non-Discord-HTTPS URL")

// isDiscordHost matches the bare host or any subdomain of Discord's media
// hosts. The leading dot matters: "evildiscordapp.com" must not pass.
// "discordapp.net" covers media.discordapp.net, where Discord serves
// attachment proxy URLs (media.discordapp.com is not a thing).
func isDiscordHost(host string) bool {
	for _, want := range []string{"discordapp.com", "discordapp.net", "discord.com", "discord.media"} {
		if host == want || strings.HasSuffix(host, "."+want) {
			return true
		}
	}
	return false
}

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

// dialGuard refuses connections to anything that is not a public unicast
// address. It runs in the dialer's Control hook, i.e. on the resolved
// address, so a hostile DNS answer cannot smuggle a private target past the
// hostname allowlist.
func dialGuard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refusing to dial malformed address %q: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// An unresolvable literal must not be waved through (the old code
		// returned nil here, which allowed an arbitrary dial).
		return fmt.Errorf("refusing to dial unparsable address %q", host)
	}
	// Unmap first: "::ffff:10.0.0.1" must be judged as the IPv4 address it
	// really is (IsPrivate covers v4-mapped forms, but the reserved-range
	// list below is v4-only, so normalize before both checks).
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

// fetchImage downloads an image attachment URL with the production safety
// rules (allowlist + dial guard + timeout + size cap).
func fetchImage(rawURL string) ([]byte, error) {
	return fetchImageWith(newFetchClient(fetchTransport()), rawURL)
}

// fetchTransport is the production transport: bounded dial timeout plus the
// address guard (defense in depth — even with the host allowlist, never dial
// private ranges, so a future CDN hostname change can't silently enable SSRF).
func fetchTransport() *http.Transport {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: dialGuard}
	return &http.Transport{DialContext: d.DialContext}
}

// newFetchClient applies the production request policy (10s timeout, redirects
// refused — a redirect off the CDN would bypass the host allowlist) to a
// caller-supplied transport, so tests can exercise that policy against a local
// server without touching the network.
func newFetchClient(tr *http.Transport) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not allowed")
		},
		Transport: tr,
	}
}

// fetchImageWith runs the fetch over a caller-supplied client (injected in
// tests, where a transport targets a local TLS server for an allowlisted host).
func fetchImageWith(client *http.Client, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, errBlockedURL
	}
	// u.Host, not u.Hostname(): Hostname() strips the ":port" suffix, so
	// "https://discordapp.com:8080/x" would pass the allowlist only to hit a
	// non-TLS service. Comparing the full authority keeps the URL exactly the
	// Discord CDN shape (and stops "discordapp.com@evil.com" style hosts,
	// whose Host is "discordapp.com@evil.com").
	if !isDiscordHost(u.Host) {
		return nil, errBlockedURL
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("unexpected status " + resp.Status)
	}
	return readCapped(resp.Body, MaxImageBytes)
}

// readCapped reads at most limit bytes and fails when the source is larger —
// the old io.ReadAll(LimitReader(…, cap+1)) silently truncated the response.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("image exceeds %d bytes", limit)
	}
	return data, nil
}
