package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// kubeClient is a read-only Kubernetes API client. It authenticates with a
// bearer token (in-cluster service account, or TROVE_KUBE_TOKEN) and only
// issues GET/list requests.
type kubeClient struct {
	http  *http.Client
	base  string
	token string
}

// kubeConfig captures how to reach the cluster and how to label it.
type kubeConfig struct {
	cluster   string // logical name used as the Trove host (default "kubernetes")
	namespace string // "" = all namespaces
}

// newKubeClient builds a client from in-cluster service-account files, or from
// env overrides for out-of-cluster use:
//
//	TROVE_KUBE_APISERVER   e.g. https://10.0.0.1:6443 (overrides in-cluster)
//	TROVE_KUBE_TOKEN       bearer token (overrides in-cluster token file)
//	TROVE_KUBE_CA          path to API server CA cert
//	TROVE_KUBE_INSECURE    "true" to skip TLS verification
//	TROVE_CLUSTER_NAME     Trove host name for this cluster (default "kubernetes")
//	TROVE_KUBE_NAMESPACE   scope to one namespace (default: all)
func newKubeClient() (*kubeClient, kubeConfig, error) {
	var kc kubeConfig
	kc.cluster = os.Getenv("TROVE_CLUSTER_NAME")
	if kc.cluster == "" {
		kc.cluster = "kubernetes"
	}
	kc.namespace = os.Getenv("TROVE_KUBE_NAMESPACE")

	base := os.Getenv("TROVE_KUBE_APISERVER")
	if base == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" {
			return nil, kc, fmt.Errorf("not in-cluster: set TROVE_KUBE_APISERVER (and TROVE_KUBE_TOKEN)")
		}
		if port == "" {
			port = "443"
		}
		base = "https://" + host + ":" + port
	}

	token := os.Getenv("TROVE_KUBE_TOKEN")
	if token == "" {
		b, err := os.ReadFile(saTokenPath)
		if err != nil {
			return nil, kc, fmt.Errorf("read service-account token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecure, _ := parseBool(os.Getenv("TROVE_KUBE_INSECURE")); insecure {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // opt-in
	} else {
		caPath := os.Getenv("TROVE_KUBE_CA")
		if caPath == "" {
			caPath = saCAPath
		}
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, kc, fmt.Errorf("read CA %s: %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, kc, fmt.Errorf("parse CA %s", caPath)
		}
		tlsCfg.RootCAs = pool
	}

	return &kubeClient{
		http:  &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
		base:  strings.TrimRight(base, "/"),
		token: token,
	}, kc, nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "", "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool %q", s)
	}
}

func (c *kubeClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kube GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("kube GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- minimal API shapes (only the fields we consume) --------------------

type objectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	OwnerReferences []ownerRef        `json:"ownerReferences"`
	Labels          map[string]string `json:"labels"`
}

type ownerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type k8sContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type workloadList struct {
	Items []workload `json:"items"`
}

type workload struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []k8sContainer `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		Replicas               int `json:"replicas"`
		ReadyReplicas          int `json:"readyReplicas"`
		DesiredNumberScheduled int `json:"desiredNumberScheduled"`
		NumberReady            int `json:"numberReady"`
	} `json:"status"`
}

type replicaSetList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
	} `json:"items"`
}

type podList struct {
	Items []pod `json:"items"`
}

type pod struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		NodeName   string         `json:"nodeName"`
		Containers []k8sContainer `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Reason     string `json:"reason"`  // pod-level, e.g. "Evicted"
		Message    string `json:"message"` // pod-level detail
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name    string `json:"name"`
			Image   string `json:"image"`
			ImageID string `json:"imageID"`
			Ready   bool   `json:"ready"`
			State   struct {
				Waiting *struct {
					Reason  string `json:"reason"` // e.g. CrashLoopBackOff, ImagePullBackOff
					Message string `json:"message"`
				} `json:"waiting"`
				Terminated *struct {
					Reason   string `json:"reason"` // e.g. Error, OOMKilled
					Message  string `json:"message"`
					ExitCode int    `json:"exitCode"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type nodeList struct {
	Items []kubeNode `json:"items"`
}

type kubeNode struct {
	Metadata objectMeta `json:"metadata"`
	Status   struct {
		Capacity    map[string]string `json:"capacity"`
		Allocatable map[string]string `json:"allocatable"`
		Conditions  []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

type nodeMetricsList struct {
	Items []struct {
		Metadata objectMeta        `json:"metadata"`
		Usage    map[string]string `json:"usage"`
	} `json:"items"`
}

// apiPath builds a list path, namespaced if configured.
func (c *collector) apiPath(group, resource string) string {
	if c.cfg.namespace != "" {
		return fmt.Sprintf("%s/namespaces/%s/%s", group, c.cfg.namespace, resource)
	}
	return group + "/" + resource
}

// k8sServerVersion is the response from GET /version (the Kubernetes discovery
// API). It carries the cluster's gitVersion, platform, and build details.
type k8sServerVersion struct {
	GitVersion string `json:"gitVersion"`
	Platform   string `json:"platform"`
	BuildDate  string `json:"buildDate"`
}

// serverVersion fetches the cluster version from the discovery API.
func (c *kubeClient) serverVersion(ctx context.Context) (k8sServerVersion, error) {
	var out k8sServerVersion
	err := c.get(ctx, "/version", &out)
	return out, err
}
