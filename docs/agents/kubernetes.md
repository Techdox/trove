# Kubernetes agent

Watches a cluster's Deployments, StatefulSets, and DaemonSets (as parent
services) and their Pods (as child instances, nested under the parent on the
dashboard). Read-only: the agent's RBAC grants only `get`/`list`/`watch`.

Run **one agent per cluster**. It runs in-cluster as a Deployment.

**Where things run:** you need a Trove **server** running somewhere first (see
the [Quickstart](../../README.md#quickstart-5-minutes)) — typically *outside*
the cluster, on your LAN. The agent runs *inside* the cluster and pushes out to
that server; nothing ever reaches into the cluster. If you only have a cluster
and no server yet, stand one up with
[`examples/docker-compose.server.yml`](../../examples/docker-compose.server.yml)
on a box on your network.

## 1. Mint a token

On the server (not in the cluster):

```sh
# Docker Compose server:
docker compose exec server trove-server agent create k8s-homelab
# bare-metal server: sudo TROVE_DB=/var/lib/trove/trove.db trove-server agent create k8s-homelab
```

## 2. Create the namespace + token secret

```sh
kubectl create namespace trove
kubectl -n trove create secret generic trove-agent \
  --from-literal=token='trove_xxxxxxxx'   # the token printed by step 1
```

## 3. Apply the manifest

Download [`deploy/kubernetes/trove-agent.yaml`](../../deploy/kubernetes/trove-agent.yaml),
set two values in the Deployment's env:

- `TROVE_SERVER_URL` — where your Trove server lives, as reachable **from your
  cluster nodes**: a LAN IP or DNS name like `http://192.168.1.50:8080`. **Not**
  `localhost` (inside a pod that's the pod itself) and **not** the Compose alias
  `http://server:8080` (that only resolves inside Compose). Getting this wrong
  is the #1 reason the cluster never appears — with no error.
- `TROVE_CLUSTER_NAME` — the host name this cluster appears under (e.g. `homelab`)

```sh
kubectl apply -f trove-agent.yaml
```

The manifest contains the ServiceAccount, a read-only ClusterRole
(deployments/statefulsets/daemonsets/replicasets/pods; get/list/watch only),
the binding, and the Deployment running
`ghcr.io/techdox/trove-agent-k8s` as nonroot with a read-only filesystem. It
also declares the `trove` namespace (harmless that step 2 created it too — we
create it first so the Secret has somewhere to live).

## 4. Verify

```sh
kubectl -n trove logs deploy/trove-agent -f
```

A healthy agent logs `report pushed hosts=1 services=…` each interval, and the
cluster appears on the dashboard within ~30s. A `connection refused`/timeout
means `TROVE_SERVER_URL` is wrong or unreachable from the cluster (see above); a
`401` means the token in the Secret doesn't match the one minted in step 1.

To upgrade the agent later: `kubectl -n trove rollout restart deploy/trove-agent`.

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
