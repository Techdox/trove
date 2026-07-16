// Package registry resolves the latest manifest digest for an image tag from a
// container registry, so the server can tell whether a running image is stale.
// It speaks the Docker Registry HTTP API v2 with the standard bearer-token
// (and basic-auth) challenge flow, using only net/http — no registry SDK.
//
// It is read-only: it issues HEAD/GET against manifest endpoints and never
// pushes, deletes, or mutates anything.
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrRateLimited signals a 429 from the registry so callers can back off harder.
var ErrRateLimited = errors.New("registry rate limited")

// Cred is a username/password for a registry host.
type Cred struct {
	Username       string   `json:"username"`
	Password       string   `json:"password"`
	AuthRealmHosts []string `json:"auth_realm_hosts,omitempty"`
}

// Options controls the registry client's outbound network boundary.
type Options struct {
	// AllowedPrivateRegistries contains exact host[:port] endpoints that may
	// resolve to RFC1918 or IPv6 ULA addresses. Loopback, link-local,
	// unspecified, and multicast addresses are always rejected.
	AllowedPrivateRegistries []string
}

// Client resolves image digests. Safe for concurrent use.
type Client struct {
	http  *http.Client
	creds map[string]Cred // keyed by registry host
}

// New builds a client with optional per-host credentials.
func New(creds map[string]Cred, options ...Options) *Client {
	privateEndpoints := make(map[string]struct{})
	for host, cred := range creds {
		if endpoint, ok := canonicalEndpoint(host); ok {
			privateEndpoints[endpoint] = struct{}{}
		}
		for _, realmHost := range cred.AuthRealmHosts {
			if endpoint, ok := canonicalEndpoint(realmHost); ok {
				privateEndpoints[endpoint] = struct{}{}
			}
		}
	}
	for _, option := range options {
		for _, host := range option.AllowedPrivateRegistries {
			if endpoint, ok := canonicalEndpoint(host); ok {
				privateEndpoints[endpoint] = struct{}{}
			}
		}
	}
	transport := &http.Transport{
		DialContext:           newSSRFGuardedDialer(privateEndpoints),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		http: &http.Client{
			Timeout:       20 * time.Second,
			Transport:     transport,
			CheckRedirect: checkRegistryRedirect,
		},
		creds: normalizeCreds(creds),
	}
}

// newSSRFGuardedDialer resolves a target once, validates every answer, then
// dials the validated IP directly. This closes the DNS-rebinding/TOCTOU gap
// between a separate safety lookup and net.Dialer's own resolution. Both
// registry URLs and attacker-controlled bearer realms/redirects pass through
// this function.
func newSSRFGuardedDialer(allowedPrivateEndpoints map[string]struct{}) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("registry: parse destination %q: %w", addr, err)
		}
		_, allowPrivate := allowedPrivateEndpoints[endpointKey(host, port)]

		var resolved []net.IPAddr
		if ip := net.ParseIP(host); ip != nil {
			resolved = []net.IPAddr{{IP: ip}}
		} else {
			resolved, err = net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("registry: resolve %q: %w", host, err)
			}
		}
		if len(resolved) == 0 {
			return nil, fmt.Errorf("registry: %q resolved to no addresses", host)
		}
		for _, candidate := range resolved {
			if err := validateDestination(candidate.IP, allowPrivate); err != nil {
				return nil, fmt.Errorf("registry: refusing destination %q (%s): %w", host, candidate.IP, err)
			}
		}

		var dialErrors []error
		for _, candidate := range resolved {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, dialErr)
		}
		return nil, fmt.Errorf("registry: connect to %q: %w", host, errors.Join(dialErrors...))
	}
}

func validateDestination(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return errors.New("invalid IP address")
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("loopback, link-local, unspecified, and multicast addresses are forbidden")
	}
	if ip.IsPrivate() && !allowPrivate {
		return errors.New("private addresses require an explicitly configured registry host")
	}
	return nil
}

func endpointKey(host, port string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	return net.JoinHostPort(host, port)
}

// canonicalEndpoint converts a configured registry host[:port] into the same
// exact key used by the dialer. Registries without a port use HTTPS/443.
func canonicalEndpoint(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "/?#@") {
		return "", false
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
			host, port = ip.String(), "443"
		} else if strings.Contains(raw, ":") {
			return "", false
		} else {
			host, port = raw, "443"
		}
	}
	if host == "" || port == "" {
		return "", false
	}
	return endpointKey(host, port), true
}

func checkRegistryRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("registry: stopped after 10 redirects")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("registry: refusing redirect to non-HTTPS URL %q", req.URL.String())
	}
	if len(via) > 0 {
		previous, previousOK := canonicalEndpoint(via[len(via)-1].URL.Host)
		next, nextOK := canonicalEndpoint(req.URL.Host)
		if !previousOK || !nextOK || previous != next {
			req.Header.Del("Authorization")
		}
	}
	return nil
}

// manifestAccept lists every manifest/index media type we can handle, so the
// registry returns the digest the tag actually points at (an image manifest for
// single-arch, or an index for multi-arch — matching what the agent captured).
var manifestAccept = []string{
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}

// LatestDigest returns the current manifest digest (sha256:...) for the image's
// tag. A HEAD is tried first (cheaper, and free of Docker Hub's pull budget),
// falling back to GET if the registry withholds the digest on HEAD.
func (c *Client) LatestDigest(ctx context.Context, ref string) (string, error) {
	reg, repo, tag := ParseImage(ref)
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", reg, repo, tag)

	digest, err := c.fetchDigest(ctx, http.MethodHead, reg, repo, manifestURL)
	if err == nil && digest != "" {
		return digest, nil
	}
	if errors.Is(err, ErrRateLimited) {
		return "", err
	}
	// Fall back to GET (some registries don't set the digest header on HEAD).
	digest, err = c.fetchDigest(ctx, http.MethodGet, reg, repo, manifestURL)
	if err != nil {
		return "", err
	}
	if digest == "" {
		return "", fmt.Errorf("registry %s: no digest for %s:%s", reg, repo, tag)
	}
	return digest, nil
}

func (c *Client) fetchDigest(ctx context.Context, method, reg, repo, manifestURL string) (string, error) {
	resp, err := c.do(ctx, method, manifestURL, "")
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		drain(resp)
		authz, aerr := c.authorize(ctx, reg, repo, challenge)
		if aerr != nil {
			return "", aerr
		}
		resp, err = c.do(ctx, method, manifestURL, authz)
		if err != nil {
			return "", err
		}
	}
	defer drain(resp)

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", ErrRateLimited
	case resp.StatusCode == http.StatusMethodNotAllowed:
		return "", nil // signal caller to try the other method
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("registry %s: %s %s -> %d", reg, method, repo, resp.StatusCode)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

func (c *Client) do(ctx context.Context, method, url, authz string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	for _, a := range manifestAccept {
		req.Header.Add("Accept", a)
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	return c.http.Do(req)
}

// authorize satisfies a WWW-Authenticate challenge, returning an Authorization
// header value. Handles both the bearer-token flow (Docker Hub, GHCR, most
// registries) and direct Basic auth.
func (c *Client) authorize(ctx context.Context, reg, repo, challenge string) (string, error) {
	scheme, params := parseChallenge(challenge)
	cred, hasCred := c.creds[reg]

	switch strings.ToLower(scheme) {
	case "basic":
		if !hasCred {
			return "", fmt.Errorf("registry %s requires credentials", reg)
		}
		return basicAuth(cred), nil

	case "bearer":
		realm := params["realm"]
		if realm == "" {
			return "", fmt.Errorf("registry %s: bearer challenge missing realm", reg)
		}
		realmURL, err := parseAuthRealm(realm)
		if err != nil {
			return "", fmt.Errorf("registry %s: invalid bearer realm: %w", reg, err)
		}
		q := realmURL.Query()
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		scope := params["scope"]
		if scope == "" {
			scope = "repository:" + repo + ":pull"
		}
		q.Set("scope", scope)
		realmURL.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, realmURL.String(), nil)
		if err != nil {
			return "", err
		}
		if hasCred && credentialAllowedForRealm(reg, realmURL.Host, cred) {
			req.Header.Set("Authorization", basicAuth(cred))
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return "", err
		}
		defer drain(resp)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("registry %s: token endpoint -> %d", reg, resp.StatusCode)
		}
		var tok struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
			return "", fmt.Errorf("registry %s: decode token: %w", reg, err)
		}
		t := tok.Token
		if t == "" {
			t = tok.AccessToken
		}
		if t == "" {
			return "", fmt.Errorf("registry %s: empty token", reg)
		}
		return "Bearer " + t, nil

	default:
		return "", fmt.Errorf("registry %s: unsupported auth scheme %q", reg, scheme)
	}
}

func parseAuthRealm(raw string) (*url.URL, error) {
	realmURL, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(realmURL.Scheme, "https") || realmURL.Host == "" {
		return nil, errors.New("realm must be an absolute HTTPS URL")
	}
	if realmURL.User != nil {
		return nil, errors.New("realm URL must not contain user information")
	}
	if realmURL.Fragment != "" {
		return nil, errors.New("realm URL must not contain a fragment")
	}
	return realmURL, nil
}

// credentialAllowedForRealm ensures registry credentials are never forwarded
// to an attacker-selected public token service. Same-endpoint realms are safe;
// Docker Hub's documented auth host is built in, and operators can explicitly
// trust additional realm endpoints per credential entry.
func credentialAllowedForRealm(registryHost, realmHost string, cred Cred) bool {
	registryEndpoint, registryOK := canonicalEndpoint(registryHost)
	realmEndpoint, realmOK := canonicalEndpoint(realmHost)
	if !registryOK || !realmOK {
		return false
	}
	if registryEndpoint == realmEndpoint {
		return true
	}
	dockerRegistry, _ := canonicalEndpoint("registry-1.docker.io")
	dockerAuth, _ := canonicalEndpoint("auth.docker.io")
	if registryEndpoint == dockerRegistry && realmEndpoint == dockerAuth {
		return true
	}
	for _, allowed := range cred.AuthRealmHosts {
		if endpoint, ok := canonicalEndpoint(allowed); ok && endpoint == realmEndpoint {
			return true
		}
	}
	return false
}

// ParseImage splits a Docker image reference into registry host, repository,
// and tag, applying Docker Hub's defaults (registry-1.docker.io, library/ for
// single-name official images, "latest" tag).
func ParseImage(ref string) (registry, repository, tag string) {
	// Drop a pinned digest if present; we resolve by tag.
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}

	registry = "registry-1.docker.io"
	name := ref
	if slash := strings.Index(ref, "/"); slash >= 0 {
		first := ref[:slash]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			registry = first
			name = ref[slash+1:]
		}
	}

	tag = "latest"
	if colon := strings.LastIndex(name, ":"); colon >= 0 {
		tag = name[colon+1:]
		name = name[:colon]
	}
	repository = name
	if registry == "registry-1.docker.io" && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	return registry, repository, tag
}

func parseChallenge(h string) (scheme string, params map[string]string) {
	params = map[string]string{}
	h = strings.TrimSpace(h)
	sp := strings.IndexByte(h, ' ')
	if sp < 0 {
		return h, params
	}
	scheme = h[:sp]
	for _, part := range splitParams(h[sp+1:]) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		params[strings.TrimSpace(kv[0])] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return scheme, params
}

// splitParams splits a challenge parameter list on commas that are not inside
// quotes.
func splitParams(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

func basicAuth(c Cred) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password))
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// normalizeCreds maps the common Docker Hub aliases to the real registry host
// so operators can key creds by "docker.io".
func normalizeCreds(in map[string]Cred) map[string]Cred {
	out := make(map[string]Cred, len(in))
	for host, cred := range in {
		out[host] = cred
		if host == "docker.io" || host == "index.docker.io" {
			out["registry-1.docker.io"] = cred
		}
	}
	return out
}
