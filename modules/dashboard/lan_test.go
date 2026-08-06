package main

import (
	"crypto/tls"
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

func TestEffectiveBaseURL(t *testing.T) {
	// public_url set → used as-is (loadConfig/Set trim the trailing slash in
	// production, so the module value is always already trimmed)
	m := mkCfgModule("0.0.0.0:8080", "https://dash.example.com")
	if got := m.effectiveBaseURL(); got != "https://dash.example.com" {
		t.Errorf("effectiveBaseURL with public_url = %q, want https://dash.example.com", got)
	}
	// no public_url → LAN URL derived from listen port
	m2 := mkCfgModule("0.0.0.0:9090", "")
	u := m2.effectiveBaseURL()
	if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":9090") {
		t.Errorf("effectiveBaseURL without public_url = %q, want http://<lan-ip>:9090", u)
	}
	if strings.Contains(u, "127.0.0.1") {
		t.Errorf("effectiveBaseURL picked loopback: %q", u)
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
