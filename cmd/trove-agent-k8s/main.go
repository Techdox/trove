// Command trove-agent-k8s discovers workloads in a Kubernetes cluster and
// pushes full-state reports to a Trove server. Deployments, StatefulSets and
// DaemonSets are reported as parent services; their Pods are child instances.
// It is read-only: it only lists/gets from the Kubernetes API.
//
// Runs in-cluster by default (service-account token + CA), or out-of-cluster
// via TROVE_KUBE_APISERVER/TROVE_KUBE_TOKEN. A read-only ClusterRole granting
// list/get on deployments, statefulsets, daemonsets, replicasets and pods is
// sufficient.
//
// Config (environment):
//
//	TROVE_SERVER_URL / TROVE_TOKEN / TROVE_INTERVAL / TROVE_AGENT_NAME  (common)
//	TROVE_CLUSTER_NAME    Trove host name for this cluster (default "kubernetes")
//	TROVE_KUBE_NAMESPACE  scope to one namespace (default: all)
//	TROVE_KUBE_APISERVER / TROVE_KUBE_TOKEN / TROVE_KUBE_CA / TROVE_KUBE_INSECURE
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/techdox/trove/internal/agentkit"
	"github.com/techdox/trove/pkg/model"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := agentkit.LoadConfig()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}
	cli, kcfg, err := newKubeClient()
	if err != nil {
		log.Error("kubernetes client", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	col := &collector{cli: cli, cfg: kcfg, log: log}
	agentkit.Run(ctx, cfg, model.PlatformKubernetes, version, col, log)
}
