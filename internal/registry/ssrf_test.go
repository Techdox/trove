package registry

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestValidateDestination(t *testing.T) {
	cases := []struct {
		name         string
		address      string
		allowPrivate bool
		blocked      bool
	}{
		{"loopback v4", "127.0.0.1", false, true},
		{"loopback v4 even when allowlisted", "127.0.0.1", true, true},
		{"loopback v6", "::1", false, true},
		{"link-local cloud metadata", "169.254.169.254", false, true},
		{"link-local multicast", "224.0.0.1", false, true},
		{"unspecified", "0.0.0.0", false, true},
		{"public IP", "104.18.121.25", false, false},
		{"RFC1918 private denied by default", "192.168.1.50", false, true},
		{"RFC1918 private explicitly allowed", "192.168.1.50", true, false},
		{"IPv6 ULA denied by default", "fd00::1", false, true},
		{"IPv6 ULA explicitly allowed", "fd00::1", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDestination(net.ParseIP(tc.address), tc.allowPrivate)
			if tc.blocked && err == nil {
				t.Fatalf("expected %s to be blocked", tc.address)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("expected %s to be allowed: %v", tc.address, err)
			}
		})
	}
}

func TestDialValidatedAddressesFallsBackAcrossIPFamilies(t *testing.T) {
	candidates := []net.IPAddr{
		{IP: net.ParseIP("2001:db8::1")},
		{IP: net.ParseIP("2001:db8::2")},
		{IP: net.ParseIP("192.0.2.10")},
	}
	attempts := make(chan string, len(candidates))
	dial := func(ctx context.Context, _, address string) (net.Conn, error) {
		attempts <- address
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if net.ParseIP(host).To4() == nil {
			<-ctx.Done() // simulate a blackholed IPv6 path
			return nil, ctx.Err()
		}
		conn, peer := net.Pipe()
		_ = peer.Close()
		return conn, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	conn, err := dialValidatedAddresses(ctx, "tcp", "443", candidates, 10*time.Millisecond, dial)
	if err != nil {
		t.Fatalf("dialValidatedAddresses: %v", err)
	}
	defer conn.Close()
	if elapsed := time.Since(started); elapsed >= 200*time.Millisecond {
		t.Fatalf("fallback took %s; blackholed first address delayed the successful candidate", elapsed)
	}

	first := <-attempts
	second := <-attempts
	firstHost, _, _ := net.SplitHostPort(first)
	secondHost, _, _ := net.SplitHostPort(second)
	if net.ParseIP(firstHost).To4() != nil || net.ParseIP(secondHost).To4() == nil {
		t.Fatalf("attempt order = %q, %q; want preferred IPv6 then fallback IPv4", first, second)
	}
}

func TestCanonicalEndpoint(t *testing.T) {
	tests := map[string]string{
		"registry.example.com":      "registry.example.com:443",
		"REGISTRY.EXAMPLE.COM:5000": "registry.example.com:5000",
		"[fd00::1]:5000":            "[fd00::1]:5000",
		"fd00::1":                   "[fd00::1]:443",
	}
	for input, want := range tests {
		got, ok := canonicalEndpoint(input)
		if !ok || got != want {
			t.Errorf("canonicalEndpoint(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "https://registry.example.com", "host/path", "host:bad:port"} {
		if got, ok := canonicalEndpoint(input); ok {
			t.Errorf("canonicalEndpoint(%q) = %q, true; want rejection", input, got)
		}
	}
}

func TestParseAuthRealmRequiresSafeHTTPSURL(t *testing.T) {
	for _, raw := range []string{
		"http://auth.example/token",
		"/relative/token",
		"https://user:password@auth.example/token",
		"https://auth.example/token#fragment",
	} {
		if _, err := parseAuthRealm(raw); err == nil {
			t.Errorf("parseAuthRealm(%q) unexpectedly succeeded", raw)
		}
	}
	got, err := parseAuthRealm("https://auth.example/token?audience=trove")
	if err != nil || got.Host != "auth.example" || got.Query().Get("audience") != "trove" {
		t.Fatalf("valid auth realm = %v, %v", got, err)
	}
}

func TestCredentialAllowedForRealm(t *testing.T) {
	cred := Cred{AuthRealmHosts: []string{"sso.example.com"}}
	tests := []struct {
		registry string
		realm    string
		want     bool
	}{
		{"ghcr.io", "ghcr.io", true},
		{"registry-1.docker.io", "auth.docker.io", true},
		{"registry.example.com", "sso.example.com", true},
		{"registry.example.com", "attacker.example", false},
	}
	for _, tt := range tests {
		if got := credentialAllowedForRealm(tt.registry, tt.realm, cred); got != tt.want {
			t.Errorf("credentialAllowedForRealm(%q, %q) = %v, want %v", tt.registry, tt.realm, got, tt.want)
		}
	}
}

func TestRegistryRedirectRequiresHTTPSAndStripsCrossOriginAuth(t *testing.T) {
	previous := &http.Request{URL: &url.URL{Scheme: "https", Host: "registry.example.com"}}
	crossOrigin := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "cdn.example.com", Path: "/manifest"},
		Header: http.Header{"Authorization": []string{"Bearer secret"}},
	}
	if err := checkRegistryRedirect(crossOrigin, []*http.Request{previous}); err != nil {
		t.Fatalf("HTTPS redirect rejected: %v", err)
	}
	if got := crossOrigin.Header.Get("Authorization"); got != "" {
		t.Fatalf("cross-origin Authorization header retained: %q", got)
	}

	insecure := &http.Request{URL: &url.URL{Scheme: "http", Host: "registry.example.com"}}
	if err := checkRegistryRedirect(insecure, []*http.Request{previous}); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("HTTP redirect error = %v, want non-HTTPS rejection", err)
	}
}
