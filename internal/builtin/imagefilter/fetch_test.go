package imagefilter

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsDiscordHost pins the allowlist: the four media hosts and their
// subdomains pass; look-alikes and suffix tricks must not.
func TestIsDiscordHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"discordapp.com", true},
		{"discordapp.net", true},
		{"discord.com", true},
		{"discord.media", true},
		// media.discordapp.net is where Discord serves attachment proxies; the
		// old allowlist omitted discordapp.net, so every fetch failed.
		{"media.discordapp.net", true},
		{"cdn.discordapp.com", true},
		{"images-ext-1.discordapp.net", true},
		{"a.b.c.discord.com", true},
		{"evildiscordapp.com", false}, // no leading dot = not a subdomain
		{"discordapp.com.evil.io", false},
		{"notdiscord.com", false},
		{"discordapp.net.evil.io", false},
		{"", false},
		{"evil.io", false},
	} {
		if got := isDiscordHost(tc.host); got != tc.want {
			t.Errorf("isDiscordHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestReadCapped covers the size cap: exactly-at-limit passes, one byte over
// fails loudly instead of silently truncating (the old behavior).
func TestReadCapped(t *testing.T) {
	data, err := readCapped(strings.NewReader("12345"), 5)
	if err != nil {
		t.Fatalf("at-limit read should succeed: %v", err)
	}
	if string(data) != "12345" {
		t.Errorf("data = %q", data)
	}
	if _, err := readCapped(strings.NewReader("123456"), 5); err == nil {
		t.Error("over-limit read must fail, not truncate")
	}
	if _, err := readCapped(strings.NewReader(""), 5); err != nil {
		t.Errorf("empty read: %v", err)
	}
	// A reader much larger than the cap must not be fully consumed/allocated.
	if _, err := readCapped(bytes.NewReader(make([]byte, MaxImageBytes+1024)), MaxImageBytes); err == nil {
		t.Error("oversized body must be rejected")
	}
}

// tlsClientFor points an allowlisted-hostname client at a local TLS server, so
// the whole fetch path (URL policy, redirect policy, cap) is exercised without
// DNS or real network access.
func tlsClientFor(srv *httptest.Server) *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(srv.URL, "https://"))
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return newFetchClient(tr)
}

func TestFetchImageWith(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("image-bytes"))
	})
	mux.HandleFunc("/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	mux.HandleFunc("/huge", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, MaxImageBytes+1))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	client := tlsClientFor(srv)

	for _, tc := range []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{"allowlisted host", "https://media.discordapp.net/ok", "image-bytes", false},
		{"server error", "https://media.discordapp.net/boom", "", true},
		{"redirect refused", "https://media.discordapp.net/redirect", "", true},
		{"over size cap", "https://media.discordapp.net/huge", "", true},
		{"http rejected", "http://media.discordapp.net/ok", "", true},
		{"host not allowlisted", "https://evil.io/ok", "", true},
		{"empty host", "https:///ok", "", true},
		{"garbage url", "ht tp://%%%", "", true},
		{"userinfo host", "https://media.discordapp.net@evil.io/ok", "", true},
		{"port suffix", "https://media.discordapp.net:8443/ok", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fetchImageWith(client, tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDialGuard pins the SSRF address guard: private, loopback, link-local,
// unspecified, multicast and the extra reserved ranges are refused, while a
// public address passes. Unparsable hosts must be refused too — the old code
// returned nil there, waving an arbitrary dial through.
func TestDialGuard(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		wantErr bool
	}{
		{"8.8.8.8:443", false},
		{"1.1.1.1:443", false},
		// IPv6 public address, port-quoted as net.SplitHostPort produces.
		{"[2606:4700:4700::1111]:443", false},
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"10.0.0.1:443", true},
		{"192.168.1.1:443", true},
		{"172.16.0.1:443", true},
		{"169.254.169.254:80", true}, // cloud metadata
		{"0.0.0.0:443", true},
		{"[fe80::1]:443", true},
		{"224.0.0.1:443", true},
		{"100.64.0.1:443", true}, // CGNAT
		{"192.0.2.1:443", true},  // TEST-NET-1
		{"198.18.0.1:443", true}, // benchmarking
		{"203.0.113.9:443", true},
		{"240.0.0.1:443", true},
		{"255.255.255.255:443", true},
		{"::ffff:10.0.0.1:443", true}, // v4-mapped private
		{"not-an-ip:443", true},       // must not be waved through
		{"no-port", true},
		{"", true},
	} {
		err := dialGuard("tcp", tc.addr, nil)
		if tc.wantErr && err == nil {
			t.Errorf("dialGuard(%q) allowed a blocked address", tc.addr)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("dialGuard(%q) = %v, want nil", tc.addr, err)
		}
	}
}
