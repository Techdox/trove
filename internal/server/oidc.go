package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig holds the configuration for OpenID Connect authentication.
// When OIDC is configured, the dashboard and read APIs require a valid
// OIDC session. Agent ingest (/api/v1/report) and /healthz are not affected.
//
// If TROVE_API_TOKEN is set, requests bearing it as a Bearer token bypass
// OIDC — this is for programmatic API access from scripts and tools that
// can't do a browser-based OAuth flow.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string

	// APIToken, if set, allows Bearer-token access to read APIs without
	// an OIDC session. Revocable by changing the env var.
	APIToken string

	// SessionMaxAge is how long a session cookie stays valid.
	// Default: 8 hours.
	SessionMaxAge time.Duration
}

// Enabled reports whether OIDC authentication is active.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// LoadOIDCConfigFromEnv reads OIDC configuration from the environment.
func LoadOIDCConfigFromEnv() OIDCConfig {
	cfg := OIDCConfig{
		Issuer:        os.Getenv("TROVE_OIDC_ISSUER"),
		ClientID:      os.Getenv("TROVE_OIDC_CLIENT_ID"),
		ClientSecret:  os.Getenv("TROVE_OIDC_CLIENT_SECRET"),
		RedirectURL:   os.Getenv("TROVE_OIDC_REDIRECT_URL"),
		APIToken:      os.Getenv("TROVE_API_TOKEN"),
		SessionMaxAge: 8 * time.Hour,
	}
	if d := os.Getenv("TROVE_OIDC_SESSION_MAX_AGE"); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil && parsed > 0 {
			cfg.SessionMaxAge = parsed
		}
	}
	return cfg
}

// ---- OIDC provider + OAuth2 config ----------------------------------------

// oidcProvider wraps the IdP provider and OAuth2 config so handlers can
// share them without re-discovering on every request.
type oidcProvider struct {
	provider *oidc.Provider
	oauth2   *oauth2.Config
	verifier *oidc.IDTokenVerifier
	cfg      OIDCConfig
	log      *slog.Logger

	// endSessionEndpoint is discovered from OIDC provider metadata when the
	// identity provider supports RP-initiated logout. When present, Trove sends
	// users there after clearing the local session so the upstream SSO session is
	// also terminated instead of immediately re-authenticating the dashboard.
	endSessionEndpoint string
}

// newOIDCProvider discovers the issuer and builds the OAuth2 config + verifier.
func newOIDCProvider(cfg OIDCConfig, log *slog.Logger) (*oidcProvider, error) {
	provider, err := oidc.NewProvider(context.Background(), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.Issuer, err)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		if log != nil {
			log.Warn("read oidc provider metadata", "err", err)
		}
	}

	return &oidcProvider{
		provider:           provider,
		oauth2:             oauthCfg,
		verifier:           verifier,
		cfg:                cfg,
		log:                log,
		endSessionEndpoint: metadata.EndSessionEndpoint,
	}, nil
}

// ---- Session cookie -------------------------------------------------------

// sessionCookie is the payload stored in the signed cookie. It's JSON-encoded,
// then HMAC-SHA256 signed with the client secret as the key.
type sessionCookie struct {
	Subject string `json:"sub"`
	Email   string `json:"email,omitempty"`
	Expires int64  `json:"exp"` // unix timestamp
}

const sessionCookieName = "trove_session"
const stateCookieName = "trove_oauth_state"

// signSession creates the signed cookie value: base64(payload).base64(hmac).
func (p *oidcProvider) signSession(s sessionCookie) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(p.cfg.ClientSecret))
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, nil
}

// verifySession validates the signed cookie and returns the session payload.
func (p *oidcProvider) verifySession(cookieValue string) (sessionCookie, error) {
	var s sessionCookie
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return s, errors.New("malformed session cookie")
	}
	encoded, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, []byte(p.cfg.ClientSecret))
	mac.Write([]byte(encoded))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return s, errors.New("invalid session cookie signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return s, fmt.Errorf("decode session payload: %w", err)
	}
	if err := json.Unmarshal(payload, &s); err != nil {
		return s, fmt.Errorf("unmarshal session: %w", err)
	}
	if time.Now().Unix() > s.Expires {
		return s, errors.New("session expired")
	}
	return s, nil
}

// setSessionCookie writes the signed session cookie to the response.
func (p *oidcProvider) setSessionCookie(w http.ResponseWriter, s sessionCookie) {
	value, err := p.signSession(s)
	if err != nil {
		p.log.Error("sign session cookie", "err", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(p.cfg.SessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   p.isSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie removes the session cookie.
func (p *oidcProvider) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   p.isSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// isSecure reports whether the redirect URL is HTTPS (cookie should be Secure).
func (p *oidcProvider) isSecure() bool {
	return strings.HasPrefix(p.cfg.RedirectURL, "https://")
}

// ---- Random state for CSRF protection -------------------------------------

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---- Middleware ------------------------------------------------------------

// requireAuth is the middleware that protects read APIs + dashboard when
// OIDC is configured. It accepts either:
//   - a valid session cookie, or
//   - a Bearer token matching TROVE_API_TOKEN (if set)
//
// If neither is present, browser requests are redirected to the IdP login
// flow; API requests (Accept: application/json or Authorization header
// present) get a 401.
func (p *oidcProvider) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check API token first (programmatic access).
		if p.cfg.APIToken != "" {
			if token, ok := bearerToken(r); ok && hmac.Equal([]byte(token), []byte(p.cfg.APIToken)) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Check session cookie.
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			if _, err := p.verifySession(cookie.Value); err == nil {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Not authenticated. Browser → redirect to login; API → 401.
		if isBrowserRequest(r) {
			// Redirect to the OIDC login endpoint with the original URL
			// as the return target.
			target := r.URL.Path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, "/oauth2/login?return="+base64.RawURLEncoding.EncodeToString([]byte(target)), http.StatusSeeOther)
			return
		}
		writeError(w, http.StatusUnauthorized, "authentication required")
	})
}

// isBrowserRequest heuristically determines if the request comes from a
// browser (should redirect to login) vs an API client (should get 401 JSON).
func isBrowserRequest(r *http.Request) bool {
	// If the client sent an Authorization header, treat it as an API client
	// even if the Accept header looks like a browser.
	if h := r.Header.Get("Authorization"); h != "" {
		return false
	}
	accept := r.Header.Get("Accept")
	// Browsers send text/html in Accept; API clients typically send
	// application/json or */* without text/html.
	return strings.Contains(accept, "text/html")
}

// safeReturnPath accepts only local absolute paths. It rejects absolute URLs,
// protocol-relative URLs (//host/path), malformed paths, and path strings that
// do not start at the dashboard root.
func safeReturnPath(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "//") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path == "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	u.Scheme = ""
	u.Host = ""
	return u.RequestURI()
}

func stateWithReturn(state, returnPath string) string {
	return state + "." + base64.RawURLEncoding.EncodeToString([]byte(safeReturnPath(returnPath)))
}

func returnPathFromState(state string) string {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "/"
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "/"
	}
	return safeReturnPath(string(decoded))
}

// ---- Handlers --------------------------------------------------------------

// handleOIDCLogin redirects the user to the IdP's authorization endpoint.
func (p *oidcProvider) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		p.log.Error("generate oauth state", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	returnPath := "/"
	if ret := r.URL.Query().Get("return"); ret != "" {
		if decoded, err := base64.RawURLEncoding.DecodeString(ret); err == nil {
			returnPath = safeReturnPath(string(decoded))
		}
	}
	state = stateWithReturn(state, returnPath)

	// Store state in a short-lived cookie for CSRF verification on callback.
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   300, // 5 minutes
		HttpOnly: true,
		Secure:   p.isSecure(),
		SameSite: http.SameSiteLaxMode,
	})
	url := p.oauth2.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// handleOIDCCallback handles the redirect from the IdP after login.
// It exchanges the auth code for tokens, verifies the ID token, and
// sets the session cookie.
func (p *oidcProvider) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state matches the cookie (CSRF protection).
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" {
		writeError(w, http.StatusBadRequest, "missing state cookie")
		return
	}
	stateParam := r.URL.Query().Get("state")
	if stateParam == "" || stateParam != stateCookie.Value {
		writeError(w, http.StatusBadRequest, "state mismatch")
		return
	}
	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   p.isSecure(),
		SameSite: http.SameSiteLaxMode,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing authorization code")
		return
	}

	// Exchange auth code for tokens.
	token, err := p.oauth2.Exchange(r.Context(), code)
	if err != nil {
		p.log.Error("oauth token exchange", "err", err)
		writeError(w, http.StatusBadGateway, "token exchange failed")
		return
	}

	// Extract and verify the ID token.
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		writeError(w, http.StatusBadGateway, "no id_token in token response")
		return
	}
	idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		p.log.Error("verify id_token", "err", err)
		writeError(w, http.StatusUnauthorized, "id_token verification failed")
		return
	}

	// Extract claims.
	var claims struct {
		Email string `json:"email"`
	}
	_ = idToken.Claims(&claims)

	// Create session.
	session := sessionCookie{
		Subject: idToken.Subject,
		Email:   claims.Email,
		Expires: time.Now().Add(p.cfg.SessionMaxAge).Unix(),
	}
	p.setSessionCookie(w, session)

	p.log.Info("oidc login", "subject", idToken.Subject, "email", claims.Email)

	// Redirect to the return URL carried in the verified OAuth state.
	returnURL := returnPathFromState(stateParam)
	http.Redirect(w, r, returnURL, http.StatusSeeOther)
}

// dashboardRootURL returns the public dashboard root derived from the OIDC
// callback URL. This is used as the post-logout return URI.
func (p *oidcProvider) dashboardRootURL() string {
	u, err := url.Parse(p.cfg.RedirectURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "/"
	}
	u.Path = "/"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// logoutRedirectURL returns the target to send the browser to after Trove has
// cleared its local session. Prefer the IdP's end-session endpoint when
// discovered; otherwise fall back to the dashboard root.
func (p *oidcProvider) logoutRedirectURL() string {
	if p.endSessionEndpoint == "" {
		return p.dashboardRootURL()
	}
	u, err := url.Parse(p.endSessionEndpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if p.log != nil {
			p.log.Warn("invalid oidc end_session_endpoint", "endpoint", p.endSessionEndpoint, "err", err)
		}
		return p.dashboardRootURL()
	}
	q := u.Query()
	q.Set("client_id", p.cfg.ClientID)
	q.Set("post_logout_redirect_uri", p.dashboardRootURL())
	u.RawQuery = q.Encode()
	return u.String()
}

// handleOIDCLogout clears the session cookie and redirects through the IdP
// logout endpoint when available. Redirecting straight back to the dashboard
// would immediately start a fresh OIDC flow and silently sign the user back in
// if the upstream SSO session was still active.
func (p *oidcProvider) handleOIDCLogout(w http.ResponseWriter, r *http.Request) {
	p.clearSessionCookie(w)
	http.Redirect(w, r, p.logoutRedirectURL(), http.StatusSeeOther)
}

// handleMe returns the current user's session info, or null when OIDC is
// not configured. The dashboard uses this to show who is signed in and
// render a sign-out button.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}

	// Try API token first.
	if s.oidc.cfg.APIToken != "" {
		if token, ok := bearerToken(r); ok && hmac.Equal([]byte(token), []byte(s.oidc.cfg.APIToken)) {
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated": true,
				"via":           "api-token",
			})
			return
		}
	}

	// Try session cookie.
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if session, err := s.oidc.verifySession(cookie.Value); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated": true,
				"via":           "oidc",
				"email":         session.Email,
				"subject":       session.Subject,
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}
