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
	"syscall"
	"time"
)

// ErrRateLimited signals a 429 from the registry so callers can back off harder.
var ErrRateLimited = errors.New("registry rate limited")

// Cred is a username/password for a registry host.
type Cred struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Client resolves image digests. Safe for concurrent use.
type Client struct {
	http  *http.Client
	creds map[string]Cred // keyed by registry host
}

// New builds a client with optional per-host credentials.
func New(creds map[string]Cred) *Client {
	return &Client{
		http:  &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: dialSSRFGuard}},
		creds: normalizeCreds(creds),
	}
}

// registryDialer rejects connections to network ranges no legitimate
// registry (or its WWW-Authenticate token endpoint) has any business being
// on. Both the image reference and the bearer-challenge "realm" URL that
// drive these requests are attacker-influenced — an agent token or a
// maliciously published image can set either — so the guard sits at the dial
// layer, where it also covers HTTP redirects (the stdlib client reuses this
// same Transport when following one).
//
// Control receives the address net/dial has already resolved DNS for, right
// before connecting — checking here (rather than resolving and checking
// separately beforehand) means there's no TOCTOU gap for a DNS answer to
// change between the check and the actual connection.
//
// RFC1918 private ranges are deliberately NOT blocked: this project's
// homelab audience runs self-hosted registries on the LAN
// (TROVE_REGISTRY_AUTHS exists specifically for that), so blocking private
// space would break a documented, legitimate use case. What IS blocked —
// loopback, link-local (which also covers every cloud metadata endpoint;
// AWS/GCP/Azure all serve theirs from 169.254.169.254), and
// unspecified/multicast — has no legitimate registry use under any
// deployment this project supports.
var registryDialer = &net.Dialer{
	Timeout: 20 * time.Second,
	Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("registry: could not parse resolved address %q", host)
		}
		if isBlockedDestination(ip) {
			return fmt.Errorf("registry: refusing to connect to %s (loopback/link-local/unspecified)", host)
		}
		return nil
	},
}

func dialSSRFGuard(ctx context.Context, network, addr string) (net.Conn, error) {
	return registryDialer.DialContext(ctx, network, addr)
}

// isBlockedDestination reports whether ip is a class of address no
// legitimate container registry (self-hosted or public) is ever reached at.
func isBlockedDestination(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast()
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
		q := url.Values{}
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		scope := params["scope"]
		if scope == "" {
			scope = "repository:" + repo + ":pull"
		}
		q.Set("scope", scope)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		if hasCred {
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
