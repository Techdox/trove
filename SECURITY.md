# Security Policy

## Model

Trove's security posture is deliberately simple in the current phase:

- **Agent ingest is authenticated**: every agent holds a per-agent bearer token
  minted by `trove-server agent create`. Tokens are 256-bit random values and
  are stored server-side only as SHA-256 hashes.
- **The dashboard and read APIs support optional OIDC authentication.** When
  all four required OIDC settings are set, the dashboard and all read APIs
  require a valid OIDC session (any standard OIDC provider — Authentik,
  Keycloak, Auth0, Google, Dex). Partial configuration fails startup and names
  the missing variables. When all authentication settings are unset, the
  dashboard is open — bind to a trusted network (LAN/VPN/tailnet) or front it
  with an authenticating reverse proxy. Agent ingest
  (`POST /api/v1/report`) and `/healthz` are never gated by OIDC. An optional
  `TROVE_API_TOKEN` allows Bearer-token access for programmatic API clients
  that can't do a browser-based OAuth flow. Logout uses the provider's OIDC
  `end_session_endpoint` when available so upstream SSO sessions are terminated
  instead of silently re-authenticating the dashboard. See
  [docs/authentication.md](docs/authentication.md).
- **Trove is read-only by design.** Agents cannot mutate the platforms they
  watch — there is no deploy/restart/exec code path anywhere. A compromised
  Trove server can see your service inventory, but cannot touch your
  workloads. A compromised agent token allows pushing (fake) reports and,
  through the image-freshness checker, causing the server to make outbound
  registry requests — see the SSRF note below for the boundary on that.
- The Docker agent needs the Docker socket mounted read-only; note that socket
  access is inherently sensitive — the agent's own API usage is GET-only, and
  the code is small enough to audit quickly (`cmd/trove-agent-docker`).
- **Image-freshness requests are guarded against SSRF, with an explicit
  allowlist for LAN registries.** The `Image` field in an agent's report
  drives an outbound registry request (`internal/registry`); a malicious
  agent token or an image pulled from an attacker-run registry could
  otherwise steer that request anywhere. The server refuses to connect to
  loopback, link-local (which covers every cloud metadata endpoint —
  169.254.169.254 on AWS/GCP/Azure alike), and unspecified/multicast
  addresses, including through the bearer-auth token endpoint and redirects.
  RFC1918 and IPv6 ULA destinations are denied unless their exact
  `host[:port]` is configured in `TROVE_REGISTRY_AUTHS` or
  `TROVE_REGISTRY_PRIVATE_HOSTS`; loopback and link-local destinations cannot
  be allowlisted. DNS answers are checked and then dialled directly to prevent
  rebinding between validation and connection. Registry credentials are not
  forwarded to an attacker-selected bearer realm; cross-host realms require an
  explicit `auth_realm_hosts` entry, except for Docker Hub's standard auth
  endpoint.
- **Free-form platform health messages are opt-in.** By default the server
  discards `health_detail` values before persistence and omits historical
  values from the services API. Setting `TROVE_HEALTH_DETAILS_ENABLED=true`
  retains the diagnostic feature, but Trove still collapses control/whitespace,
  redacts common bearer token and named-secret forms, and caps the stored value.
  Structured health/state/exit-code fields remain available without this flag.

## Reporting a vulnerability

Please report suspected vulnerabilities privately via
[GitHub Security Advisories](https://github.com/techdox/trove/security/advisories/new)
rather than opening a public issue. Reports will be acknowledged as quickly as
possible — this is a spare-time project, so please allow a reasonable window
before public disclosure.
