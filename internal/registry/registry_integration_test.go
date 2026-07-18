package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type rewriteRegistryTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteRegistryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL = new(url.URL)
	*clone.URL = *req.URL
	clone.Host = req.URL.Host
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func integrationRegistryClient(t *testing.T, creds map[string]Cred, handler http.Handler) (*Client, func()) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	target, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("parse test server URL: %v", err)
	}
	transport := server.Client().Transport
	client := &Client{
		http: &http.Client{
			Transport:     rewriteRegistryTransport{target: target, base: transport},
			Timeout:       2 * time.Second,
			CheckRedirect: checkRegistryRedirect,
		},
		creds: normalizeCreds(creds),
	}
	return client, server.Close
}

func TestLatestDigestBearerAuthenticationIntegration(t *testing.T) {
	var mu sync.Mutex
	var manifestAuth []string
	tokenAuth := ""
	tokenScope := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "registry.example.test":
			mu.Lock()
			manifestAuth = append(manifestAuth, r.Header.Get("Authorization"))
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer pull-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://auth.example.test/token",service="registry.example.test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:bearer")
			w.WriteHeader(http.StatusOK)
		case "auth.example.test":
			tokenAuth = r.Header.Get("Authorization")
			tokenScope = r.URL.Query().Get("scope")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"pull-token"}`))
		default:
			http.Error(w, "unexpected host", http.StatusBadRequest)
		}
	})
	client, closeServer := integrationRegistryClient(t, map[string]Cred{
		"registry.example.test": {
			Username:       "robot",
			Password:       "secret",
			AuthRealmHosts: []string{"auth.example.test"},
		},
	}, handler)
	defer closeServer()

	digest, err := client.LatestDigest(context.Background(), "registry.example.test/acme/widget:v2")
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if digest != "sha256:bearer" {
		t.Fatalf("digest = %q", digest)
	}
	if tokenAuth != basicAuth(Cred{Username: "robot", Password: "secret"}) {
		t.Fatalf("token endpoint Authorization = %q", tokenAuth)
	}
	if tokenScope != "repository:acme/widget:pull" {
		t.Fatalf("token scope = %q", tokenScope)
	}
	mu.Lock()
	gotManifestAuth := append([]string(nil), manifestAuth...)
	mu.Unlock()
	if len(gotManifestAuth) != 2 || gotManifestAuth[0] != "" || gotManifestAuth[1] != "Bearer pull-token" {
		t.Fatalf("manifest authorization sequence = %v", gotManifestAuth)
	}
}

func TestLatestDigestBasicAuthRedirectStripsCredentials(t *testing.T) {
	var registryRequests int
	cdnAuthorization := "not-called"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "registry.example.test":
			registryRequests++
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != basicAuth(Cred{Username: "robot", Password: "secret"}) {
				http.Error(w, "wrong basic auth", http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "https://cdn.example.test/manifests/widget", http.StatusTemporaryRedirect)
		case "cdn.example.test":
			cdnAuthorization = r.Header.Get("Authorization")
			w.Header().Set("Docker-Content-Digest", "sha256:redirect")
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected host", http.StatusBadRequest)
		}
	})
	client, closeServer := integrationRegistryClient(t, map[string]Cred{
		"registry.example.test": {Username: "robot", Password: "secret"},
	}, handler)
	defer closeServer()

	digest, err := client.LatestDigest(context.Background(), "registry.example.test/acme/widget:v2")
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if digest != "sha256:redirect" || registryRequests != 2 {
		t.Fatalf("digest = %q, registry requests = %d", digest, registryRequests)
	}
	if cdnAuthorization != "" {
		t.Fatalf("cross-origin redirect leaked Authorization: %q", cdnAuthorization)
	}
}

func TestLatestDigestFallsBackToGETIntegration(t *testing.T) {
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			w.Header().Set("Docker-Content-Digest", "sha256:get")
		}
		w.WriteHeader(http.StatusOK)
	})
	client, closeServer := integrationRegistryClient(t, nil, handler)
	defer closeServer()

	digest, err := client.LatestDigest(context.Background(), "registry.example.test/acme/widget:v2")
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if digest != "sha256:get" || len(methods) != 2 || methods[0] != http.MethodHead || methods[1] != http.MethodGet {
		t.Fatalf("digest = %q, methods = %v", digest, methods)
	}
}

func TestLatestDigestRejectsInsecureRedirectIntegration(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://registry.example.test/plaintext", http.StatusTemporaryRedirect)
	})
	client, closeServer := integrationRegistryClient(t, nil, handler)
	defer closeServer()

	_, err := client.LatestDigest(context.Background(), "registry.example.test/acme/widget:v2")
	if err == nil || !strings.Contains(err.Error(), "refusing redirect to non-HTTPS") {
		t.Fatalf("LatestDigest error = %v", err)
	}
}

func TestLatestDigestDoesNotForwardCredentialsToUntrustedRealm(t *testing.T) {
	tokenAuthorization := "not-called"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "registry.example.test":
			if r.Header.Get("Authorization") != "Bearer anonymous-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://untrusted.example.test/token"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:anonymous")
			w.WriteHeader(http.StatusOK)
		case "untrusted.example.test":
			tokenAuthorization = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"access_token":"anonymous-token"}`))
		default:
			http.Error(w, "unexpected host", http.StatusBadRequest)
		}
	})
	client, closeServer := integrationRegistryClient(t, map[string]Cred{
		"registry.example.test": {Username: "robot", Password: "secret"},
	}, handler)
	defer closeServer()

	digest, err := client.LatestDigest(context.Background(), "registry.example.test/acme/widget:v2")
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if digest != "sha256:anonymous" || tokenAuthorization != "" {
		t.Fatalf("digest = %q, untrusted token Authorization = %q", digest, tokenAuthorization)
	}
}

func TestLatestDigestRateLimitStopsBeforeGET(t *testing.T) {
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	client, closeServer := integrationRegistryClient(t, nil, handler)
	defer closeServer()

	_, err := client.LatestDigest(context.Background(), "registry.example.test/acme/widget:v2")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("LatestDigest error = %v, want ErrRateLimited", err)
	}
	if len(methods) != 1 || methods[0] != http.MethodHead {
		t.Fatalf("rate-limited methods = %v, want HEAD only", methods)
	}
}

func TestLatestDigestSSRFGuardBlocksLoopbackEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := New(nil).LatestDigest(ctx, "127.0.0.1:443/acme/widget:v2")
	if err == nil || !strings.Contains(err.Error(), "refusing destination") || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("LatestDigest error = %v, want loopback SSRF rejection", err)
	}

	_, err = New(nil, Options{AllowedPrivateRegistries: []string{"127.0.0.1:443"}}).
		LatestDigest(ctx, "127.0.0.1:443/acme/widget:v2")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("allowlisted loopback error = %v, want unconditional rejection", err)
	}
}
