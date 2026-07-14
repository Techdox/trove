package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// testProvider builds an oidcProvider without doing real OIDC discovery,
// so tests can exercise the cookie/middleware logic in isolation.
func testProvider(t *testing.T) *oidcProvider {
	t.Helper()
	return &oidcProvider{
		oauth2: &oauth2.Config{
			ClientID:    "test-client",
			RedirectURL: "https://trove.example/oauth2/callback",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://idp.example/auth", TokenURL: "https://idp.example/token"},
			Scopes:      []string{"openid", "profile", "email"},
		},
		cfg: OIDCConfig{
			Issuer:        "https://idp.example",
			ClientID:      "test-client",
			ClientSecret:  "test-secret",
			RedirectURL:   "https://trove.example/oauth2/callback",
			APIToken:      "test-api-token",
			SessionMaxAge: 8 * time.Hour,
		},
		log: nil,
	}
}

func TestSessionCookieSignAndVerify(t *testing.T) {
	p := testProvider(t)
	original := sessionCookie{
		Subject: "user-123",
		Email:   "nick@example.com",
		Expires: time.Now().Add(1 * time.Hour).Unix(),
	}
	signed, err := p.signSession(original)
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}

	got, err := p.verifySession(signed)
	if err != nil {
		t.Fatalf("verifySession: %v", err)
	}
	if got.Subject != original.Subject {
		t.Errorf("subject = %q, want %q", got.Subject, original.Subject)
	}
	if got.Email != original.Email {
		t.Errorf("email = %q, want %q", got.Email, original.Email)
	}
}

func TestSessionCookieRejectsTampered(t *testing.T) {
	p := testProvider(t)
	original := sessionCookie{
		Subject: "user-123",
		Expires: time.Now().Add(1 * time.Hour).Unix(),
	}
	signed, err := p.signSession(original)
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}

	// Flip the last character of the signature.
	tampered := signed[:len(signed)-1]
	if signed[len(signed)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}

	_, err = p.verifySession(tampered)
	if err == nil {
		t.Fatal("verifySession should fail for tampered cookie")
	}
}

func TestSessionCookieRejectsExpired(t *testing.T) {
	p := testProvider(t)
	expired := sessionCookie{
		Subject: "user-123",
		Expires: time.Now().Add(-1 * time.Hour).Unix(),
	}
	signed, err := p.signSession(expired)
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}

	_, err = p.verifySession(signed)
	if err == nil {
		t.Fatal("verifySession should fail for expired session")
	}
}

func TestSessionCookieRejectsGarbage(t *testing.T) {
	p := testProvider(t)
	cases := []string{
		"",
		"garbage",
		"garbage.notsig",
		"%",
	}
	for _, c := range cases {
		_, err := p.verifySession(c)
		if err == nil {
			t.Errorf("verifySession(%q) should fail", c)
		}
	}
}

func TestRequireAuthAllowsAPIToken(t *testing.T) {
	p := testProvider(t)
	called := false
	handler := p.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	req.Header.Set("Authorization", "Bearer test-api-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("handler not called despite valid API token")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireAuthRejectsBadAPIToken(t *testing.T) {
	p := testProvider(t)
	handler := p.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestRequireAuthAllowsValidSession(t *testing.T) {
	p := testProvider(t)
	called := false
	handler := p.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	session := sessionCookie{
		Subject: "user-123",
		Expires: time.Now().Add(1 * time.Hour).Unix(),
	}
	signed, err := p.signSession(session)
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: signed})
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("handler not called despite valid session")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireAuthRedirectsBrowser(t *testing.T) {
	p := testProvider(t)
	handler := p.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect)", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc[:len("/oauth2/login")] != "/oauth2/login" {
		t.Errorf("Location = %q, want /oauth2/login...", loc)
	}
}

func TestRequireAuthReturns401ForAPIRequest(t *testing.T) {
	p := testProvider(t)
	handler := p.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestIsBrowserRequest(t *testing.T) {
	cases := []struct {
		accept      string
		auth        string
		wantBrowser bool
	}{
		{"text/html", "", true},
		{"application/json", "", false},
		{"text/html", "Bearer foo", false}, // auth header → API client
		{"*/*", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		got := isBrowserRequest(req)
		if got != c.wantBrowser {
			t.Errorf("isBrowserRequest(accept=%q, auth=%q) = %v, want %v", c.accept, c.auth, got, c.wantBrowser)
		}
	}
}

func TestSafeReturnPath(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"root", "/", "/"},
		{"path with query", "/api/v1/services?status=unhealthy", "/api/v1/services?status=unhealthy"},
		{"relative", "api/v1/services", "/"},
		{"absolute url", "https://evil.example/phish", "/"},
		{"protocol relative", "//evil.example/phish", "/"},
		{"triple slash", "///evil.example/phish", "/"},
		{"javascript", "javascript:alert(1)", "/"},
		{"empty", "", "/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := safeReturnPath(c.raw); got != c.want {
				t.Fatalf("safeReturnPath(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestHandleOIDCLoginCarriesSafeReturnInState(t *testing.T) {
	p := testProvider(t)
	ret := base64.RawURLEncoding.EncodeToString([]byte("/api/v1/services?status=unhealthy"))
	req := httptest.NewRequest("GET", "/oauth2/login?return="+ret, nil)
	w := httptest.NewRecorder()
	p.handleOIDCLogin(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	state := u.Query().Get("state")
	if got := returnPathFromState(state); got != "/api/v1/services?status=unhealthy" {
		t.Fatalf("state return path = %q", got)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != stateCookieName || cookie.Value != state {
		t.Fatalf("state cookie = %s/%q, want %s/%q", cookie.Name, cookie.Value, stateCookieName, state)
	}
}

func TestHandleOIDCLoginSanitizesUnsafeReturnInState(t *testing.T) {
	p := testProvider(t)
	ret := base64.RawURLEncoding.EncodeToString([]byte("//evil.example/phish"))
	req := httptest.NewRequest("GET", "/oauth2/login?return="+ret, nil)
	w := httptest.NewRecorder()
	p.handleOIDCLogin(w, req)

	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if got := returnPathFromState(u.Query().Get("state")); got != "/" {
		t.Fatalf("unsafe return path = %q, want /", got)
	}
}

func TestLoadOIDCConfigFromEnv(t *testing.T) {
	t.Setenv("TROVE_OIDC_ISSUER", "https://idp.example")
	t.Setenv("TROVE_OIDC_CLIENT_ID", "client-123")
	t.Setenv("TROVE_OIDC_CLIENT_SECRET", "secret-456")
	t.Setenv("TROVE_OIDC_REDIRECT_URL", "https://trove.example/oauth2/callback")
	t.Setenv("TROVE_API_TOKEN", "api-token-789")

	cfg := LoadOIDCConfigFromEnv()
	if !cfg.Enabled() {
		t.Fatal("OIDC should be enabled")
	}
	if cfg.Issuer != "https://idp.example" {
		t.Errorf("Issuer = %q", cfg.Issuer)
	}
	if cfg.ClientID != "client-123" {
		t.Errorf("ClientID = %q", cfg.ClientID)
	}
	if cfg.APIToken != "api-token-789" {
		t.Errorf("APIToken = %q", cfg.APIToken)
	}
	if cfg.SessionMaxAge != 8*time.Hour {
		t.Errorf("SessionMaxAge = %v", cfg.SessionMaxAge)
	}
}

func TestLoadOIDCConfigFromEnvUnset(t *testing.T) {
	// Clear all OIDC env vars
	for _, key := range []string{"TROVE_OIDC_ISSUER", "TROVE_OIDC_CLIENT_ID", "TROVE_OIDC_CLIENT_SECRET", "TROVE_OIDC_REDIRECT_URL", "TROVE_API_TOKEN"} {
		os.Unsetenv(key)
	}
	cfg := LoadOIDCConfigFromEnv()
	if cfg.Enabled() {
		t.Fatal("OIDC should be disabled when env vars are unset")
	}
}

func TestOIDCConfigEnabledValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  OIDCConfig
		want bool
	}{
		{"all set", OIDCConfig{Issuer: "a", ClientID: "b", ClientSecret: "c", RedirectURL: "d"}, true},
		{"missing issuer", OIDCConfig{ClientID: "b", ClientSecret: "c", RedirectURL: "d"}, false},
		{"missing client ID", OIDCConfig{Issuer: "a", ClientSecret: "c", RedirectURL: "d"}, false},
		{"missing secret", OIDCConfig{Issuer: "a", ClientID: "b", RedirectURL: "d"}, false},
		{"missing redirect", OIDCConfig{Issuer: "a", ClientID: "b", ClientSecret: "c"}, false},
		{"all empty", OIDCConfig{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.Enabled(); got != c.want {
				t.Errorf("Enabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestConfigureOIDCAllowsAllAuthenticationSettingsUnset(t *testing.T) {
	srv := New(nil, nil)
	if err := srv.ConfigureOIDC(OIDCConfig{}); err != nil {
		t.Fatalf("ConfigureOIDC: %v", err)
	}
	if srv.oidc != nil {
		t.Fatal("OIDC provider configured for empty settings")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("open-mode /api/v1/me status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestConfigureOIDCRejectsEveryPartialRequiredCombination(t *testing.T) {
	settings := []struct {
		name  string
		value string
		set   func(*OIDCConfig, string)
	}{
		{"TROVE_OIDC_ISSUER", "https://idp.example", func(cfg *OIDCConfig, value string) { cfg.Issuer = value }},
		{"TROVE_OIDC_CLIENT_ID", "client", func(cfg *OIDCConfig, value string) { cfg.ClientID = value }},
		{"TROVE_OIDC_CLIENT_SECRET", "secret", func(cfg *OIDCConfig, value string) { cfg.ClientSecret = value }},
		{"TROVE_OIDC_REDIRECT_URL", "https://trove.example/oauth2/callback", func(cfg *OIDCConfig, value string) { cfg.RedirectURL = value }},
	}

	for mask := 1; mask < (1<<len(settings))-1; mask++ {
		t.Run(fmt.Sprintf("set_%04b", mask), func(t *testing.T) {
			cfg := OIDCConfig{}
			var missing []string
			for i, setting := range settings {
				if mask&(1<<i) != 0 {
					setting.set(&cfg, setting.value)
				} else {
					missing = append(missing, setting.name)
				}
			}

			srv := New(nil, nil)
			err := srv.ConfigureOIDC(cfg)
			if err == nil {
				t.Fatal("ConfigureOIDC accepted partial configuration")
			}
			for _, name := range missing {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name missing setting %s", err, name)
				}
			}
			if srv.oidc != nil {
				t.Fatal("OIDC provider configured after validation failure")
			}
		})
	}
}

func TestConfigureOIDCRejectsAPITokenWithoutOIDC(t *testing.T) {
	srv := New(nil, nil)
	err := srv.ConfigureOIDC(OIDCConfig{APIToken: "api-token"})
	if err == nil {
		t.Fatal("ConfigureOIDC accepted API token without OIDC")
	}
	for _, name := range []string{
		"TROVE_OIDC_ISSUER",
		"TROVE_OIDC_CLIENT_ID",
		"TROVE_OIDC_CLIENT_SECRET",
		"TROVE_OIDC_REDIRECT_URL",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name missing setting %s", err, name)
		}
	}
}

func TestConfigureOIDCAcceptsCompleteConfiguration(t *testing.T) {
	var issuer string
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/keys",
		}); err != nil {
			t.Errorf("encode discovery response: %v", err)
		}
	}))
	issuer = discovery.URL
	t.Cleanup(discovery.Close)

	srv := New(nil, nil)
	err := srv.ConfigureOIDC(OIDCConfig{
		Issuer:       issuer,
		ClientID:     "client",
		ClientSecret: "secret",
		RedirectURL:  "https://trove.example/oauth2/callback",
	})
	if err != nil {
		t.Fatalf("ConfigureOIDC: %v", err)
	}
	if srv.oidc == nil {
		t.Fatal("OIDC provider not configured")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("protected API status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestOIDCDiscoveryHonorsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		discovery.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := newOIDCProvider(ctx, OIDCConfig{Issuer: discovery.URL}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("newOIDCProvider error = %v, want context deadline exceeded", err)
	}
}

func TestDashboardRootURLFromRedirectURL(t *testing.T) {
	p := testProvider(t)
	p.cfg.RedirectURL = "https://trove.example/oauth2/callback?ignored=1#frag"

	got := p.dashboardRootURL()
	if got != "https://trove.example/" {
		t.Errorf("dashboardRootURL() = %q, want https://trove.example/", got)
	}
}

func TestLogoutRedirectURLUsesEndSessionEndpoint(t *testing.T) {
	p := testProvider(t)
	p.endSessionEndpoint = "https://idp.example/application/o/trove/end-session/"

	got := p.logoutRedirectURL()
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse logout redirect URL %q: %v", got, err)
	}
	if u.Scheme != "https" || u.Host != "idp.example" || u.Path != "/application/o/trove/end-session/" {
		t.Fatalf("logout redirect URL = %q, want IdP end-session endpoint", got)
	}
	if q := u.Query().Get("client_id"); q != "test-client" {
		t.Errorf("client_id = %q, want test-client", q)
	}
	if q := u.Query().Get("post_logout_redirect_uri"); q != "https://trove.example/" {
		t.Errorf("post_logout_redirect_uri = %q, want https://trove.example/", q)
	}
}

func TestLogoutRedirectURLFallsBackToDashboardRoot(t *testing.T) {
	p := testProvider(t)

	if got := p.logoutRedirectURL(); got != "https://trove.example/" {
		t.Errorf("logoutRedirectURL without end-session endpoint = %q, want dashboard root", got)
	}

	p.endSessionEndpoint = "://bad-url"
	if got := p.logoutRedirectURL(); got != "https://trove.example/" {
		t.Errorf("logoutRedirectURL with invalid end-session endpoint = %q, want dashboard root", got)
	}
}

func TestHandleOIDCLogoutClearsSessionAndRedirectsToEndSession(t *testing.T) {
	p := testProvider(t)
	p.endSessionEndpoint = "https://idp.example/application/o/trove/end-session/"

	req := httptest.NewRequest("POST", "/oauth2/logout", nil)
	w := httptest.NewRecorder()
	p.handleOIDCLogout(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}
	if u.Host != "idp.example" || u.Query().Get("post_logout_redirect_uri") != "https://trove.example/" {
		t.Fatalf("Location = %q, want IdP logout with dashboard return", loc)
	}

	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			cleared = c.MaxAge < 0 && c.Value == ""
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the session cookie")
	}
}

// TestSessionCookieManualVerify tests the HMAC construction directly, so if
// the sign/verify pair has a bug the test gives a clear error rather than
// both sides being wrong in the same way.
func TestSessionCookieManualVerify(t *testing.T) {
	p := testProvider(t)
	session := sessionCookie{
		Subject: "manual-test",
		Expires: time.Now().Add(1 * time.Hour).Unix(),
	}

	payload, _ := json.Marshal(session)
	encoded := base64.RawURLEncoding.EncodeToString(payload)

	mac := hmac.New(sha256.New, []byte(p.cfg.ClientSecret))
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	cookieValue := encoded + "." + sig

	got, err := p.verifySession(cookieValue)
	if err != nil {
		t.Fatalf("manual verify: %v", err)
	}
	if got.Subject != "manual-test" {
		t.Errorf("subject = %q", got.Subject)
	}
}
