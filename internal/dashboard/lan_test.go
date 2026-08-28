package dashboard

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// mkCfgModule builds a DashboardModule whose effectiveListen/effectivePublicURL
// read from the module config (ctx is nil → coreConfig() returns nil, so the
// core config.yml is never touched).
func mkCfgModule(listen, publicURL string) *DashboardModule {
	return &DashboardModule{
		cfg: &DashboardConfig{Listen: listen, PublicURL: publicURL},
		mu:  sync.Mutex{},
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true,
		"LOCALHOST": true, "0.0.0.0": false, "192.168.1.5": false, "10.0.0.2": false, "": false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestListenPort(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8080": "8080", "0.0.0.0:9090": "9090", ":3000": "3000",
		"127.0.0.1:0": "8080", "bad-addr-no-port": "8080", "": "8080",
	}
	for listen, want := range cases {
		m := mkCfgModule(listen, "")
		if got := m.listenPort(); got != want {
			t.Errorf("listenPort(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestLoopbackOnlyListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true,
		"0.0.0.0:8080": false, ":8080": false, "192.168.1.5:8080": false,
		"bad-addr-no-port": true,
	}
	for listen, want := range cases {
		m := mkCfgModule(listen, "")
		if got := m.loopbackOnlyListen(); got != want {
			t.Errorf("loopbackOnlyListen(%q) = %v, want %v", listen, got, want)
		}
	}
}

// cidrAddr builds a *net.IPNet with the HOST address ip (ParseCIDR would mask
// it to the network address), matching what net.InterfaceAddrs returns.
func cidrAddr(t *testing.T, ip string, prefix int) net.Addr {
	t.Helper()
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("ParseIP(%q) failed", ip)
	}
	return &net.IPNet{IP: parsed, Mask: net.CIDRMask(prefix, 32)}
}

func TestLANIPFromAddrs(t *testing.T) {
	dockerBridge := cidrAddr(t, "172.17.0.1", 16)
	dockerCompose := cidrAddr(t, "172.18.0.1", 16)
	cgnat := cidrAddr(t, "100.64.0.1", 10)
	tailscale := cidrAddr(t, "100.100.100.100", 10)
	loopback := cidrAddr(t, "127.0.0.1", 8)
	lan192 := cidrAddr(t, "192.168.1.5", 24)
	lan10 := cidrAddr(t, "10.0.0.7", 8)

	cases := []struct {
		name  string
		addrs []net.Addr
		want  string
	}{
		{"docker bridge first must not shadow the LAN", []net.Addr{dockerBridge, dockerCompose, cgnat, lan192}, "192.168.1.5"},
		{"10/8 also preferred", []net.Addr{lan10, dockerCompose}, "10.0.0.7"},
		{"no preferred range → first non-excluded fallback", []net.Addr{dockerBridge, dockerCompose}, "172.18.0.1"},
		{"only excluded/loopback → empty", []net.Addr{loopback, dockerBridge, cgnat, tailscale}, ""},
		{"empty input → empty", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lanIPFromAddrs(c.addrs); got != c.want {
				t.Errorf("lanIPFromAddrs() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExcludedPreferredLANIP(t *testing.T) {
	excluded := map[string]bool{
		"172.17.0.1": true, "172.17.255.255": true, // docker default bridge
		"100.64.0.1": true, "100.127.255.255": true, // CGNAT (Tailscale et al.)
		"100.63.255.255": false, "100.128.0.1": false, // just outside CGNAT
		"172.18.0.1": false, "192.168.1.5": false, "10.0.0.7": false, "8.8.8.8": false,
	}
	for ip, want := range excluded {
		if got := isExcludedLANIP(net.ParseIP(ip)); got != want {
			t.Errorf("isExcludedLANIP(%s) = %v, want %v", ip, got, want)
		}
	}
	preferred := map[string]bool{
		"192.168.1.5": true, "192.168.0.1": true, "10.0.0.7": true,
		"172.16.0.1": false, "172.18.0.1": false, "100.64.0.1": false, "8.8.8.8": false,
	}
	for ip, want := range preferred {
		if got := isPreferredLANIP(net.ParseIP(ip)); got != want {
			t.Errorf("isPreferredLANIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

// TestLanURLConcreteHost covers the lanURL host resolution: a concrete
// non-loopback listen host is used verbatim (IPv4 and IPv6, correctly
// bracketed via net.JoinHostPort); wildcard/loopback binds fall back to the
// detected LAN address, whose exact value is machine-dependent (only the URL
// shape is asserted there).
func TestLanURLConcreteHost(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{"192.168.1.5:8080", "http://192.168.1.5:8080"},
		{"[fd00::1]:8080", "http://[fd00::1]:8080"}, // IPv6 stays bracketed
		{"dash.local:8443", "http://dash.local:8443"},
	}
	for _, c := range cases {
		m := mkCfgModule(c.listen, "")
		if got := m.lanURL(); got != c.want {
			t.Errorf("lanURL(%q) = %q, want %q", c.listen, got, c.want)
		}
	}

	// wildcard binds → detected LAN address (shape only)
	for _, listen := range []string{"0.0.0.0:9090", "[::]:9090", ":9090"} {
		m := mkCfgModule(listen, "")
		u := m.lanURL()
		if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":9090") || len(u) <= len("http://:9090") {
			t.Errorf("lanURL(%q) = %q, want http://<lan-ip>:9090", listen, u)
		}
	}
}

// TestLANIPFallback stubs both detection seams to prove the documented
// 127.0.0.1 fallback when no default route and no usable interface address
// exist (e.g. isolated CI runners).
func TestLANIPFallback(t *testing.T) {
	oldRoute, oldAddrs := primaryLANIP, interfaceAddrs
	t.Cleanup(func() { primaryLANIP, interfaceAddrs = oldRoute, oldAddrs })
	primaryLANIP = func() string { return "" }
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			cidrAddr(t, "127.0.0.1", 8),
			cidrAddr(t, "172.17.0.1", 16),
			cidrAddr(t, "100.64.0.1", 10),
		}, nil
	}
	m := mkCfgModule("0.0.0.0:8080", "")
	if got := m.lanIP(); got != "127.0.0.1" {
		t.Errorf("lanIP() = %q, want documented 127.0.0.1 fallback", got)
	}
}

func TestEffectiveBaseURL(t *testing.T) {
	// public_url set → used as-is (loadConfig/Set trim the trailing slash in
	// production, so the module value is always already trimmed)
	m := mkCfgModule("0.0.0.0:8080", "https://dash.example.com")
	if got := m.effectiveBaseURL(); got != "https://dash.example.com" {
		t.Errorf("effectiveBaseURL with public_url = %q, want https://dash.example.com", got)
	}
	// no public_url → LAN URL derived from the listen port (host is
	// machine-dependent; the documented loopback fallback is valid)
	m2 := mkCfgModule("0.0.0.0:9090", "")
	u := m2.effectiveBaseURL()
	if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":9090") {
		t.Errorf("effectiveBaseURL without public_url = %q, want http://<host>:9090", u)
	}
}

func TestRedirectBaseURL(t *testing.T) {
	// public_url set → overrides the request origin
	m := mkCfgModule("0.0.0.0:8080", "https://dash.example.com")
	r := &http.Request{Host: "192.168.1.5:8080"}
	if got := m.redirectBaseURL(r); got != "https://dash.example.com" {
		t.Errorf("redirectBaseURL with public_url = %q, want https://dash.example.com", got)
	}

	// no public_url → derived from the request origin (http)
	m2 := mkCfgModule("0.0.0.0:8080", "")
	if got := m2.redirectBaseURL(&http.Request{Host: "192.168.1.5:8080"}); got != "http://192.168.1.5:8080" {
		t.Errorf("redirectBaseURL (http) = %q, want http://192.168.1.5:8080", got)
	}

	// direct TLS → https
	rTLS := &http.Request{Host: "dash.local:8443", TLS: &tls.ConnectionState{}}
	if got := m2.redirectBaseURL(rTLS); got != "https://dash.local:8443" {
		t.Errorf("redirectBaseURL (TLS) = %q, want https://dash.local:8443", got)
	}

	// TLS-terminating proxy → X-Forwarded-Proto wins
	rProxy := &http.Request{Host: "dash.example.com", Header: http.Header{"X-Forwarded-Proto": []string{"https"}}}
	if got := m2.redirectBaseURL(rProxy); got != "https://dash.example.com" {
		t.Errorf("redirectBaseURL (proxy) = %q, want https://dash.example.com", got)
	}

	// empty Host → LAN fallback
	if got := m2.redirectBaseURL(&http.Request{}); !strings.HasPrefix(got, "http://") {
		t.Errorf("redirectBaseURL (empty host) = %q, want http://<lan-ip>:<port>", got)
	}
}

func TestRequestIsHTTPS(t *testing.T) {
	m := mkCfgModule("0.0.0.0:8080", "https://dash.example.com") // public_url https must NOT force Secure on http origins
	if m.requestIsHTTPS(&http.Request{Host: "192.168.1.5:8080"}) {
		t.Error("plain-http request must not be treated as HTTPS")
	}
	if !m.requestIsHTTPS(&http.Request{Host: "x", TLS: &tls.ConnectionState{}}) {
		t.Error("direct TLS request must be HTTPS")
	}
	if !m.requestIsHTTPS(&http.Request{Host: "x", Header: http.Header{"X-Forwarded-Proto": []string{"https"}}}) {
		t.Error("X-Forwarded-Proto: https must be HTTPS")
	}
}
