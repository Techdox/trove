# Security Policy

## Model

Trove's security posture is deliberately simple in the current phase:

- **Agent ingest is authenticated**: every agent holds a per-agent bearer token
  minted by `trove-server agent create`. Tokens are 256-bit random values and
  are stored server-side only as SHA-256 hashes.
- **The dashboard and read APIs are NOT authenticated.** Run the server on a
  trusted network (LAN, VPN, tailnet) or behind an authenticating reverse
  proxy. Do not expose it directly to the internet. Native OIDC is on the
  roadmap.
- **Trove is read-only by design.** Agents cannot mutate the platforms they
  watch — there is no deploy/restart/exec code path anywhere. A compromised
  Trove server can see your service inventory, but cannot touch your
  workloads. A compromised agent token allows pushing (fake) reports and,
  through the image-freshness checker, causing the server to make outbound
  registry requests — see the SSRF note below for the boundary on that.
- The Docker agent needs the Docker socket mounted read-only; note that socket
  access is inherently sensitive — the agent's own API usage is GET-only, and
  the code is small enough to audit quickly (`cmd/trove-agent-docker`).
- **Image-freshness requests are guarded against SSRF, with an intentional
  carve-out for LAN registries.** The `Image` field in an agent's report
  drives an outbound registry request (`internal/registry`); a malicious
  agent token or an image pulled from an attacker-run registry could
  otherwise steer that request anywhere. The server refuses to connect to
  loopback, link-local (which covers every cloud metadata endpoint —
  169.254.169.254 on AWS/GCP/Azure alike), and unspecified/multicast
  addresses, including through the bearer-auth token endpoint's
  attacker-influenced redirect. **RFC1918 private ranges are deliberately
  still reachable** — self-hosted registries on your LAN are a supported,
  documented use case (`TROVE_REGISTRY_AUTHS`) — so a compromised token can
  still cause the server to probe other hosts on its own local network.
  Treat agent tokens accordingly: they are not fully untrusted input.

## Reporting a vulnerability

Please report suspected vulnerabilities privately via
[GitHub Security Advisories](https://github.com/techdox/trove/security/advisories/new)
rather than opening a public issue. Reports will be acknowledged as quickly as
possible — this is a spare-time project, so please allow a reasonable window
before public disclosure.
