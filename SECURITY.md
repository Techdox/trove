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
  workloads. A compromised agent token allows only pushing (fake) reports.
- The Docker agent needs the Docker socket mounted read-only; note that socket
  access is inherently sensitive — the agent's own API usage is GET-only, and
  the code is small enough to audit quickly (`cmd/trove-agent-docker`).

## Reporting a vulnerability

Please report suspected vulnerabilities privately via
[GitHub Security Advisories](https://github.com/techdox/trove/security/advisories/new)
rather than opening a public issue. Reports will be acknowledged as quickly as
possible — this is a spare-time project, so please allow a reasonable window
before public disclosure.
