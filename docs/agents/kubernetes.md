# Kubernetes agent

Watches a cluster's Deployments, StatefulSets, and DaemonSets (as parent
services) and their Pods (as child instances, nested under the parent on the
dashboard). Read-only: the agent's RBAC grants only `get`/`list`/`watch`.

Run **one agent per cluster**. It runs in-cluster as a Deployment.

## 1. Mint a token

```sh
trove-server agent create k8s-homelab
```

## 2. Create the namespace + token secret

```sh
kubectl create namespace trove
kubectl -n trove create secret generic trove-agent \
  --from-literal=token='trove_xxxxxxxx'
```

## 3. Apply the manifest

Download [`deploy/kubernetes/trove-agent.yaml`](../../deploy/kubernetes/trove-agent.yaml),
set two values in the Deployment's env:

- `TROVE_SERVER_URL` — where your Trove server lives (the agent pushes out;
  the server never needs to reach into the cluster)
- `TROVE_CLUSTER_NAME` — the host name this cluster appears under (e.g. `homelab`)

```sh
kubectl apply -f trove-agent.yaml
```

The manifest contains the ServiceAccount, a read-only ClusterRole
(deployments/statefulsets/daemonsets/replicasets/pods; get/list/watch only),
the binding, and the Deployment running
`ghcr.io/techdox/trove-agent-k8s` as nonroot with a read-only filesystem.

## Configuration

| Variable               | Default        | Purpose                                       |
| ---------------------- | -------------- | --------------------------------------------- |
| `TROVE_SERVER_URL`     | _(required)_   | Base URL of the Trove server.                 |
| `TROVE_TOKEN`          | _(required)_   | Token from `agent create` (via the secret).   |
| `TROVE_CLUSTER_NAME`   | `kubernetes`   | Dashboard host name for this cluster.         |
| `TROVE_KUBE_NAMESPACE` | _(all)_        | Restrict discovery to one namespace.          |
| `TROVE_INTERVAL`       | `30s`          | Push interval.                                |

Out-of-cluster use (rare — e.g. watching a cluster from a jump host) is
supported via `TROVE_KUBE_APISERVER`, `TROVE_KUBE_TOKEN`, `TROVE_KUBE_CA`, and
`TROVE_KUBE_INSECURE`.

## What you'll see

- Each workload shows `ready/desired` (e.g. `2/2`) as its state and rolls up
  health: `healthy` when all replicas are ready, `unhealthy` otherwise.
- Pods appear nested under their workload (pod → ReplicaSet → Deployment
  ownership is resolved automatically), with per-pod phase and readiness.
- Bare pods and Job pods (no workload owner) appear as top-level services.
- Pod image digests are captured, so image freshness works for cluster
  workloads just like containers.
