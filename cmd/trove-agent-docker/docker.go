package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// dockerClient is a deliberately minimal, READ-ONLY client for the Docker
// Engine API. It only ever issues GET requests — there is no method that
// mutates Docker state, which is how Trove's read-only guarantee is enforced
// structurally rather than by convention. It speaks the daemon's default API
// version (unversioned paths) for broad compatibility.
type dockerClient struct {
	http *http.Client
	base string // scheme+host prefix, e.g. "http://docker" (unix) or "http://host:2375"
}

// newDockerClient connects using DOCKER_HOST, defaulting to the local unix
// socket. Supports unix:// and tcp:// endpoints (npipe is out of scope for
// Phase 1).
func newDockerClient(dockerHost string) (*dockerClient, error) {
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(dockerHost)
	if err != nil {
		return nil, fmt.Errorf("parse DOCKER_HOST %q: %w", dockerHost, err)
	}

	switch u.Scheme {
	case "unix":
		socket := u.Path
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}
		return &dockerClient{
			http: &http.Client{Transport: tr, Timeout: 30 * time.Second},
			base: "http://docker", // host is ignored for unix transport
		}, nil
	case "tcp", "http", "https":
		scheme := "http"
		if u.Scheme == "https" {
			scheme = "https"
		}
		return &dockerClient{
			http: &http.Client{Timeout: 30 * time.Second},
			base: scheme + "://" + u.Host,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q", u.Scheme)
	}
}

// get issues a GET against the Docker API and decodes the JSON response into
// out. This is the only request verb the client exposes.
func (c *dockerClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- minimal API response shapes (only the fields we consume) ------------

type dockerPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type dockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`  // created|running|paused|restarting|removing|exited|dead
	Status  string            `json:"Status"` // human string, may carry "(healthy)"
	Ports   []dockerPort      `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
}

type dockerInspect struct {
	State struct {
		Health *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
			Log           []struct {
				ExitCode int    `json:"ExitCode"`
				Output   string `json:"Output"`
			} `json:"Log"`
		} `json:"Health"`
		ExitCode int    `json:"ExitCode"`
		Error    string `json:"Error"`
	} `json:"State"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

type dockerImage struct {
	RepoDigests []string `json:"RepoDigests"`
}

type dockerInfo struct {
	Name          string `json:"Name"`
	ServerVersion string `json:"ServerVersion"`
}

// ---- typed calls ---------------------------------------------------------

func (c *dockerClient) listContainers(ctx context.Context) ([]dockerContainer, error) {
	var out []dockerContainer
	if err := c.get(ctx, "/containers/json?all=1", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *dockerClient) inspectContainer(ctx context.Context, id string) (dockerInspect, error) {
	var out dockerInspect
	err := c.get(ctx, "/containers/"+id+"/json", &out)
	return out, err
}

func (c *dockerClient) inspectImage(ctx context.Context, id string) (dockerImage, error) {
	var out dockerImage
	err := c.get(ctx, "/images/"+id+"/json", &out)
	return out, err
}

func (c *dockerClient) info(ctx context.Context) (dockerInfo, error) {
	var out dockerInfo
	err := c.get(ctx, "/info", &out)
	return out, err
}

// ping verifies the daemon is reachable.
func (c *dockerClient) ping(ctx context.Context) error {
	return c.get(ctx, "/_ping", nil)
}
