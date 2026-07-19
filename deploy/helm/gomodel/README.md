# GoModel Helm Chart

A Helm chart for [GoModel](https://github.com/ENTERPILOT/GoModel) — a high-performance,
lightweight AI gateway that routes requests to multiple AI model providers through an
OpenAI-compatible API.

## TL;DR

```bash
helm install gomodel oci://registry-1.docker.io/enterpilot/gomodel \
  --version 0.1.0 \
  --namespace gomodel --create-namespace \
  --set secrets.masterKey=$(openssl rand -hex 32) \
  --set secrets.data.OPENAI_API_KEY=sk-...
```

> Replace `enterpilot` with the Docker Hub namespace the chart was published under
> (`DOCKER_USERNAME`). The chart is pushed by the `helm-release.yml` workflow when a
> `helm-v*` tag is created.
>
> You can also install directly from a local checkout:
> `helm install gomodel ./deploy/helm/gomodel -n gomodel --create-namespace`.

## Introduction

The chart deploys GoModel using the official distroless image
(`enterpilot/gomodel`, non-root UID/GID `65532`) with Kubernetes best practices:
read-only root filesystem, dropped capabilities, HTTP health/readiness probes,
optional autoscaling, PodDisruptionBudget, NetworkPolicy and Prometheus integration.

## Deployment modes

GoModel loads configuration in three layers: **built-in defaults → `config.yaml`
(optional) → environment variables (always win)**.

| Mode | When | Workload | Scaling |
| --- | --- | --- | --- |
| **Stateless** (default) | External Postgres/MongoDB + Redis | `Deployment` | Multi-replica, HPA |
| **SQLite** | `persistence.enabled=true` | `StatefulSet` + PVC at `/app/data` | Single replica only |

The default (no external DB configured) runs a single replica writing SQLite to an
**ephemeral** `emptyDir` — suitable for evaluation only. For production, either enable
`persistence` (durable single node) or point the app at external datastores (scalable).

## Configuration

Two complementary mechanisms:

- `config` — rendered verbatim into a ConfigMap and mounted read-only at
  `/app/config/config.yaml`. Supports `${ENV_VAR}` expansion, so reference secret
  values by name here and provide them through `secrets`.
- `env` / `extraEnv` — environment variables, which always override `config.yaml`.

### Secrets

Sensitive values (`GOMODEL_MASTER_KEY`, `<PROVIDER>_API_KEY`, `POSTGRES_URL`,
`MONGODB_URL`, `REDIS_URL`, ...) go under `secrets`:

```yaml
secrets:
  masterKey: "change-me"          # GOMODEL_MASTER_KEY; without it the gateway is UNSAFE
  data:
    OPENAI_API_KEY: sk-...
    POSTGRES_URL: postgres://user:pass@host:5432/gomodel
    REDIS_URL: redis://redis:6379
```

They are rendered into a chart-managed `Secret` and injected via `envFrom`. For
production / external secret managers, set `secrets.existingSecret` to reference a
pre-created Secret instead.

## Health & metrics

- **Liveness**: `GET {basePath}/health`
- **Readiness**: `GET {basePath}/health/ready` (returns `503` when primary storage is
  down, pulling the pod out of the Service; a degraded cache stays in rotation)
- **Metrics**: `GET {basePath}/metrics` — enable with `metrics.enabled=true` (or
  `metrics.serviceMonitor.enabled=true`, which also creates a Prometheus Operator
  ServiceMonitor)

Probe paths automatically honor `BASE_PATH` / `config.server.base_path`.

## Values

See [`values.yaml`](./values.yaml) for the full, documented list. Key values:

| Key | Default | Description |
| --- | --- | --- |
| `image.repository` | `enterpilot/gomodel` | Image repository |
| `image.tag` | `""` (chart `appVersion`) | Image tag |
| `replicaCount` | `1` | Replicas (forced to 1 in SQLite mode) |
| `persistence.enabled` | `false` | Use SQLite StatefulSet + PVC |
| `secrets.masterKey` | `""` | Gateway master key |
| `secrets.existingSecret` | `""` | Reference an existing Secret instead |
| `config` | `{}` | Rendered into `config.yaml` |
| `ingress.enabled` | `false` | Create an Ingress |
| `autoscaling.enabled` | `false` | Create an HPA (stateless only) |
| `podDisruptionBudget.enabled` | `false` | Create a PDB |
| `networkPolicy.enabled` | `false` | Restrict ingress/egress |
| `metrics.enabled` | `false` | Enable Prometheus `/metrics` |
| `metrics.serviceMonitor.enabled` | `false` | Create a ServiceMonitor |

## Example value sets

Ready-to-use examples live in [`ci/`](./ci):

- `stateless-values.yaml` — multi-replica with external Postgres + Redis
- `sqlite-persistent-values.yaml` — single node with a persistent SQLite volume
- `ingress-tls-values.yaml` — Ingress + TLS + NetworkPolicy + ServiceMonitor
