package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/techdox/trove/internal/agentkit"
	"github.com/techdox/trove/pkg/model"
)

// collector turns the Docker daemon's view into a Trove report. Docker is a
// single-host platform, so Collect returns exactly one HostSnapshot.
type collector struct {
	cli *dockerClient
	log *slog.Logger
}

// Collect builds a full-state snapshot of every container on the host.
func (c *collector) Collect(ctx context.Context) ([]agentkit.HostSnapshot, error) {
	containers, err := c.cli.listContainers(ctx)
	if err != nil {
		return nil, err
	}

	hostname, meta := c.hostInfo(ctx)

	// Cache image digests within this cycle so N containers of the same image
	// cost one image inspect, not N.
	digestCache := map[string]string{}

	services := make([]model.ReportService, 0, len(containers))
	for _, ct := range containers {
		insp, err := c.cli.inspectContainer(ctx, ct.ID)
		if err != nil {
			// Container likely vanished between list and inspect, or a
			// transient error. Fall back to list-only data.
			c.log.Debug("inspect container failed", "id", short(ct.ID), "err", err)
		}

		health := mapHealth(ct.State, insp)
		services = append(services, model.ReportService{
			ExternalID:   ct.ID,
			Name:         containerName(ct.Names),
			Kind:         model.KindContainer,
			Image:        ct.Image,
			ImageDigest:  c.resolveDigest(ctx, ct.ImageID, digestCache),
			State:        ct.State,
			Health:       health,
			HealthDetail: healthDetail(ct.State, insp, health),
			Ports:        mapPorts(ct.Ports),
			Labels:       ct.Labels,
		})
	}

	return []agentkit.HostSnapshot{{
		Host:     model.ReportHost{Hostname: hostname, Meta: meta},
		Services: services,
	}}, nil
}

// hostInfo returns the hostname and platform metadata, falling back to the OS
// hostname if the daemon info call fails. Meta keys use the dotted convention
// (docker.version, docker.api_version, docker.os, docker.arch) matching the
// Proxmox agent's proxmox.version pattern.
func (c *collector) hostInfo(ctx context.Context) (string, map[string]string) {
	info, err := c.cli.info(ctx)
	if err != nil {
		c.log.Debug("docker info failed; using OS hostname", "err", err)
		h, _ := os.Hostname()
		return h, nil
	}
	hostname := info.Name
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	meta := map[string]string{}
	if info.ServerVersion != "" {
		meta["docker.version"] = info.ServerVersion
	}
	// The /version endpoint carries API version and OS/arch details.
	v, err := c.cli.version(ctx)
	if err != nil {
		c.log.Debug("docker version failed", "err", err)
	} else {
		if v.APIVersion != "" {
			meta["docker.api_version"] = v.APIVersion
		}
		if v.OS != "" {
			meta["docker.os"] = v.OS
		} else if info.OSType != "" {
			meta["docker.os"] = info.OSType
		}
		if v.Arch != "" {
			meta["docker.arch"] = v.Arch
		} else if info.Arch != "" {
			meta["docker.arch"] = info.Arch
		}
	}
	return hostname, meta
}

// resolveDigest returns the registry manifest digest (sha256:...) for an image,
// caching by image id. Empty for locally-built images with no repo digest —
// Phase 2 image-freshness needs the registry digest, not the local config id,
// so we don't substitute a misleading value.
func (c *collector) resolveDigest(ctx context.Context, imageID string, cache map[string]string) string {
	if imageID == "" {
		return ""
	}
	if d, ok := cache[imageID]; ok {
		return d
	}
	d := ""
	img, err := c.cli.inspectImage(ctx, imageID)
	if err != nil {
		c.log.Debug("inspect image failed", "id", short(imageID), "err", err)
	} else if len(img.RepoDigests) > 0 {
		rd := img.RepoDigests[0]
		if at := strings.LastIndex(rd, "@"); at >= 0 {
			d = rd[at+1:]
		}
	}
	cache[imageID] = d
	return d
}

// mapHealth normalizes Docker's health into the Trove enum, following the
// Phase 1 rules: prefer the healthcheck verdict; otherwise a stopped container
// is only "unhealthy" if its restart policy meant it to stay up.
func mapHealth(state string, insp dockerInspect) model.Health {
	if insp.State.Health != nil {
		switch insp.State.Health.Status {
		case "healthy":
			return model.HealthHealthy
		case "unhealthy":
			return model.HealthUnhealthy
		default: // "starting", "none", ""
			return model.HealthUnknown
		}
	}
	switch state {
	case "running":
		return model.HealthUnknown
	case "exited", "dead":
		switch insp.HostConfig.RestartPolicy.Name {
		case "always", "unless-stopped":
			return model.HealthUnhealthy
		default:
			return model.HealthUnknown
		}
	default: // created, paused, restarting, removing
		return model.HealthUnknown
	}
}

// healthDetail explains WHY a container is unhealthy, so the dashboard can show
// more than a red badge. It prefers the failing healthcheck's last output;
// failing that (a stopped container that was meant to stay up), it reports the
// exit code and any daemon error. Empty for healthy/unknown states.
func healthDetail(state string, insp dockerInspect, health model.Health) string {
	if health != model.HealthUnhealthy {
		return ""
	}
	const maxLen = 300
	if h := insp.State.Health; h != nil && len(h.Log) > 0 {
		last := h.Log[len(h.Log)-1]
		out := collapseWS(last.Output)
		if len(out) > maxLen {
			out = out[:maxLen] + "…"
		}
		if out != "" {
			return "healthcheck failing (exit " + itoa(last.ExitCode) + "): " + out
		}
		return "healthcheck failing (exit " + itoa(last.ExitCode) + ", " + itoa(h.FailingStreak) + " in a row)"
	}
	if state == "exited" || state == "dead" {
		d := "container " + state + " (exit " + itoa(insp.State.ExitCode) + ")"
		if insp.State.Error != "" {
			if e := collapseWS(insp.State.Error); e != "" {
				d += ": " + e
			}
		}
		return d
	}
	return ""
}

// collapseWS trims and collapses all runs of whitespace (including the newlines
// Docker healthcheck output usually carries) into single spaces.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func itoa(n int) string { return strconv.Itoa(n) }

// mapPorts converts Docker port entries to model ports, de-duplicating the
// IPv4/IPv6 pairs Docker reports for a single published mapping.
func mapPorts(ports []dockerPort) []model.Port {
	if len(ports) == 0 {
		return nil
	}
	seen := map[[3]int]struct{}{}
	out := make([]model.Port, 0, len(ports))
	for _, p := range ports {
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		key := [3]int{p.PublicPort, p.PrivatePort, protoKey(proto)}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model.Port{
			Host:      p.PublicPort,
			Container: p.PrivatePort,
			Proto:     proto,
		})
	}
	return out
}

func protoKey(proto string) int {
	switch proto {
	case "tcp":
		return 1
	case "udp":
		return 2
	case "sctp":
		return 3
	default:
		return 0
	}
}

func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
