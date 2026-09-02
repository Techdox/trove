package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/pkg/model"
)

const maxCreateAgentBytes = 16 << 10 // 16 KiB

type createAgentRequest struct {
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	ServerURL string `json:"server_url"`
}

type createAgentResponse struct {
	Name      string `json:"name"`
	Token     string `json:"token"`
	Platform  string `json:"platform"`
	ServerURL string `json:"server_url"`
	Snippet   string `json:"snippet"`
	Format    string `json:"format"`
	Filename  string `json:"filename,omitempty"`
	Guide     string `json:"guide"`
}

// handleCreateAgent mints a one-time agent token and returns a copy-paste
// install snippet for the chosen platform. This is a Trove-catalogue write
// (not a workload mutation): it does not talk to Docker, Kubernetes, Proxmox,
// or systemd. Auth matches the other dashboard APIs.
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateAgentBytes)

	var req createAgentRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 16 KiB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 16 KiB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: trailing data")
		return
	}

	platform := model.Platform(strings.TrimSpace(strings.ToLower(req.Platform)))
	if !platform.Valid() {
		writeError(w, http.StatusBadRequest, "platform must be docker, kubernetes, proxmox, or local")
		return
	}
	serverURL, err := sanitizeInstallServerURL(req.ServerURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	token, agent, err := s.store.CreateAgent(r.Context(), req.Name)
	if errors.Is(err, store.ErrAgentExists) {
		writeError(w, http.StatusConflict, "agent with that name already exists")
		return
	}
	if errors.Is(err, store.ErrInvalidAgentName) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.log.Error("create agent", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}

	snippet, format, filename := installSnippet(platform, serverURL, token)
	s.log.Info("agent created from dashboard", "agent", agent.Name, "platform", platform)
	writeJSON(w, http.StatusCreated, createAgentResponse{
		Name:      agent.Name,
		Token:     token,
		Platform:  string(platform),
		ServerURL: serverURL,
		Snippet:   snippet,
		Format:    format,
		Filename:  filename,
		Guide:     installGuide(platform),
	})
}

type deleteAgentResponse struct {
	OK   bool   `json:"ok"`
	Name string `json:"name"`
}

// handleDeleteAgent removes an agent and its catalogue data. It does not
// contact the platform the agent used to observe.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := store.ValidateAgentName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.DeleteAgent(r.Context(), name); errors.Is(err, store.ErrAgentNotFound) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	} else if err != nil {
		s.log.Error("delete agent", "agent", name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to delete agent")
		return
	}
	s.log.Info("agent deleted from dashboard", "agent", name)
	writeJSON(w, http.StatusOK, deleteAgentResponse{OK: true, Name: name})
}

func sanitizeInstallServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("server_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("server_url must be an http(s) URL")
	}
	if u.User != nil {
		return "", errors.New("server_url must not include credentials")
	}
	u.Fragment = ""
	u.RawQuery = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func installGuide(platform model.Platform) string {
	switch platform {
	case model.PlatformDocker:
		return "docs/agents/docker.md"
	case model.PlatformKubernetes:
		return "docs/agents/kubernetes.md"
	case model.PlatformProxmox:
		return "docs/agents/proxmox.md"
	case model.PlatformLocal:
		return "docs/agents/local.md"
	default:
		return "docs/agents/"
	}
}

func installSnippet(platform model.Platform, serverURL, token string) (snippet, format, filename string) {
	switch platform {
	case model.PlatformDocker:
		return dockerInstallSnippet(serverURL, token), "yaml", "docker-compose.yml"
	case model.PlatformProxmox:
		return proxmoxInstallSnippet(serverURL, token), "yaml", "docker-compose.yml"
	case model.PlatformKubernetes:
		return kubernetesInstallSnippet(serverURL, token), "shell", ""
	default:
		return localInstallSnippet(serverURL, token), "shell", ""
	}
}

func dockerInstallSnippet(serverURL, token string) string {
	return fmt.Sprintf(`# Additional Docker host. Run this on the machine you want to watch,
# not on the Trove server (unless this is that same box).
services:
  agent:
    image: ghcr.io/techdox/trove-agent-docker:latest
    environment:
      TROVE_SERVER_URL: %q
      TROVE_TOKEN: %q
    volumes:
      # Read-only socket mount; the agent only ever issues GETs to Docker.
      - /var/run/docker.sock:/var/run/docker.sock:ro
    restart: unless-stopped
`, serverURL, token)
}

func proxmoxInstallSnippet(serverURL, token string) string {
	return fmt.Sprintf(`# Additional Proxmox agent. Create a read-only PVE API token first
# (PVEAuditor). See docs/agents/proxmox.md.
services:
  agent:
    image: ghcr.io/techdox/trove-agent-proxmox:latest
    environment:
      TROVE_SERVER_URL: %q
      TROVE_TOKEN: %q
      TROVE_PROXMOX_URL: "https://YOUR-PVE-HOST:8006"
      TROVE_PROXMOX_TOKEN: "trove@pve!trove-agent=YOUR-TOKEN-SECRET"
    restart: unless-stopped
`, serverURL, token)
}

func kubernetesInstallSnippet(serverURL, token string) string {
	return fmt.Sprintf(`# Additional Kubernetes cluster. The agent is in-cluster and read-only.
kubectl create namespace trove --dry-run=client -o yaml | kubectl apply -f -
kubectl -n trove create secret generic trove-agent --from-literal=token=%q

# Then apply deploy/kubernetes/trove-agent.yaml from the Trove repo after
# setting TROVE_SERVER_URL to the address this cluster can reach:
#   %s
# Full walkthrough: docs/agents/kubernetes.md
`, token, serverURL)
}

func localInstallSnippet(serverURL, token string) string {
	return fmt.Sprintf(`# Additional Linux host. The local agent runs on the host, not in a container.
# Grab the archive for your arch from https://github.com/techdox/trove/releases
# VERSION=<release>  # e.g. 0.17.1
# curl -fLO "https://github.com/techdox/trove/releases/download/v${VERSION}/trove-agent-local_${VERSION}_linux_amd64.tar.gz"
# tar xzf trove-agent-local_${VERSION}_linux_amd64.tar.gz
# sudo install -m 0755 trove-agent-local /usr/local/bin/
# sudo cp deploy/systemd/trove-agent-local.service /etc/systemd/system/

sudo tee /etc/trove-agent-local.env >/dev/null <<'EOF'
TROVE_SERVER_URL=%s
TROVE_TOKEN=%s
EOF
sudo chmod 600 /etc/trove-agent-local.env
sudo systemctl enable --now trove-agent-local
`, serverURL, token)
}
