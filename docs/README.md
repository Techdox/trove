# Trove documentation

Repository documentation is versioned with the code and is the source of truth
for behaviour that can change between releases.

## Ownership

| Surface | Owns | Does not own |
| --- | --- | --- |
| [Root README](../README.md) | Product overview, supported installation paths, and quickstarts. | Detailed operational, upgrade, authentication, API, configuration, or security procedures. |
| [Configuration](configuration.md) | Complete environment-variable reference. | Provider walkthroughs and install commands. |
| Repository docs | Agent operation, authentication behaviour, API contracts, alerts, upgrades, backup/recovery, and release security for this code version. | General discovery content or screenshot-heavy walkthroughs. |
| [Wiki](https://github.com/Techdox/trove/wiki) | Non-versioned walkthroughs, screenshots, examples, and community-oriented discovery guides. | Canonical configuration, authentication, upgrade, recovery, API, or security instructions. |

Wiki pages should link to the relevant repository document instead of copying
version-specific commands or configuration. A repository change that alters
operator behaviour must update the owning repository document in the same pull
request.

## Guides

- Agents: [Docker](agents/docker.md), [Kubernetes](agents/kubernetes.md),
  [Proxmox](agents/proxmox.md), [Linux/systemd](agents/local.md), and
  [credential rotation](agents/rotation.md).
- [Configuration](configuration.md).
- [Dashboard authentication](authentication.md).
- [API and metrics](api.md).
- [Alerts and digest](alerts.md).
- [Upgrades, backup, restore rehearsal, and rollback](upgrades.md).
- [Release integrity and provenance](release-security.md).
- Project-wide vulnerability and trust boundaries: [SECURITY.md](../SECURITY.md).

## Keeping the boundary intact

CI runs `scripts/check_docs_ownership.py`. It confirms canonical cross-links,
keeps operational command blocks out of the README's upgrade and authentication
sections, and rejects duplicated multi-line command examples shared between the
README and their versioned operational guides.
