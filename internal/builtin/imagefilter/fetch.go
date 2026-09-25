package imagefilter

import (
	"errors"
	"io"
	"net"
	"net/http"
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
func isDiscordHost(host string) bool {
	for _, want := range []string{"discordapp.com", "discord.com", "discord.media"} {
		if host == want || strings.HasSuffix(host, "."+want) {
			return true
		}
	}
	return false
}

// fetchImage downloads an image attachment URL with the safety rules above.
func fetchImage(rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, errBlockedURL
	}
	if !isDiscordHost(u.Hostname()) {
		return nil, errBlockedURL
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errors.New("redirects are not allowed")
		},
		// Defense in depth: even with the host allowlist, never dial private
		// ranges (a future CDN hostname change can't silently enable SSRF).
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
				Control: func(network, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					ip := net.ParseIP(host)
					if ip == nil {
						return nil // hostname dial (proxy scenarios); allowlist already vetted it
					}
					if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
						return errors.New("refusing to dial private address")
					}
					return nil
				},
			}).DialContext,
		},
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
	return io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
}
