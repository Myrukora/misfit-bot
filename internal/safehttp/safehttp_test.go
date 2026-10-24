package safehttp

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestClientRejectsInsecureURLs pins the request policy: only HTTPS on port
// 443 is fetched, so an http:// embed URL can never become an outbound request.
func TestClientRejectsInsecureURLs(t *testing.T) {
	c := NewClient(time.Second)
	for _, raw := range []string{
		"http://example.com/image.png",
		"https://example.com:8443/image.png",
	} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("new request %q: %v", raw, err)
		}
		if _, err := c.Do(req); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("Do(%q) error = %v, want ErrBlockedURL", raw, err)
		}
	}
}

// TestDialGuardRejectsNonPublicAddresses pins the dial-time SSRF guard that
// catches hostile DNS answers the URL checks cannot see.
func TestDialGuardRejectsNonPublicAddresses(t *testing.T) {
	blocked := []string{
		"127.0.0.1:443",
		"[::1]:443",
		"10.1.2.3:443",
		"192.168.1.10:443",
		"169.254.169.254:443", // link-local metadata endpoint
		"100.64.0.1:443",      // CGNAT
		"::ffff:10.0.0.1:443", // v4-mapped private
	}
	for _, addr := range blocked {
		if err := dialGuard("tcp", addr, nil); err == nil {
			t.Errorf("dialGuard(%q) allowed a non-public address", addr)
		}
	}
	if err := dialGuard("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("dialGuard(public) = %v, want nil", err)
	}
}
