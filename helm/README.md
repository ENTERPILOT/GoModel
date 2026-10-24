# GoModel Helm chart

Deploys [GoModel](https://gomodel.enterpilot.io), an OpenAI-compatible AI gateway, on Kubernetes.

## Quick start

```bash
helm install gomodel ./helm -n gomodel --create-namespace \
  --set secretEnv.GOMODEL_MASTER_KEY=change-me \
  --set secretEnv.OPENAI_API_KEY=sk-...

kubectl -n gomodel port-forward svc/gomodel 8080:8080
curl -H "Authorization: Bearer change-me" http://127.0.0.1:8080/v1/models
```

The default install runs one pod with SQLite on a 1Gi PersistentVolumeClaim,
Prometheus metrics on `/metrics`, a read-only root filesystem, and the image's
non-root user (UID 65532).

## Configuration

GoModel is configured entirely through environment variables. Any variable from
[`.env.template`](../.env.template) goes in one of two maps:

| Value       | Stored in | Use for                                            |
| ----------- | --------- | -------------------------------------------------- |
| `env`       | ConfigMap | Plain settings: `LOG_LEVEL`, `REDIS_URL`, `BASE_PATH`, … |
| `secretEnv` | Secret    | `GOMODEL_MASTER_KEY`, provider API keys, database URLs |

Changing either rolls the pods. To keep secrets out of Helm values, create the
Secret yourself and reference it:

```yaml
extraEnvFrom:
  - secretRef:
      name: gomodel-keys   # keys: GOMODEL_MASTER_KEY, OPENAI_API_KEY, ...
```

Set `GOMODEL_MASTER_KEY`. Without it the gateway accepts unauthenticated requests
and the install notes print a warning.

### config.yaml

Use `config` for settings that env vars cannot express (per-provider resilience,
custom provider names, model allowlists). It is mounted at
`/app/config/config.yaml`; env vars still override it.

```yaml
config: |
  providers:
    openai-eu:
      type: openai
      api_key: ${OPENAI_EU_API_KEY}
      base_url: https://eu.api.openai.com/v1
```

Or point `existingConfigMap` at a ConfigMap that has a `config.yaml` key.

### Storage and replicas

| `storage.type` | Replicas | Data location                                  |
| -------------- | -------- | ---------------------------------------------- |
| `sqlite`       | 1        | PVC at `/app/data` (`persistence.enabled: true`) |
| `postgresql`   | any      | `storage.url` or `POSTGRES_URL`                |
| `mongodb`      | any      | `storage.url` or `MONGODB_URL`                 |

SQLite is per pod, so the chart refuses `replicaCount > 1` or autoscaling until
you switch to PostgreSQL or MongoDB:

```yaml
replicaCount: 3
storage:
  type: postgresql
  url: postgres://gomodel:secret@postgresql:5432/gomodel
```

`storage.url` lands in the chart Secret. If the URL already lives in a Secret of
your own, load it through `extraEnvFrom` or an `extraEnv` entry named
`POSTGRES_URL` / `MONGODB_URL` instead and leave `storage.url` empty.

`helm uninstall` deletes the claim with the release. To keep the database, add
`persistence.annotations: {helm.sh/resource-policy: keep}`.

With SQLite on a ReadWriteOnce volume the Deployment uses the `Recreate`
strategy, so upgrades briefly stop the gateway. Set `persistence.enabled: false`
for a throwaway install; the data then lives on an emptyDir and is lost when the
pod is replaced.

### Exposing the gateway

Ingress:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
  hosts:
    - host: gomodel.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: gomodel-tls
      hosts: [gomodel.example.com]
```

Gateway API:

```yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: public-gateway
      namespace: gateway-system
  hostnames: [gomodel.example.com]
```

To serve under a path prefix set `env.BASE_PATH: /g`; probe, metrics and
HTTPRoute paths follow automatically.

### Metrics

`metrics.enabled` (default `true`) exposes `/metrics` without authentication.
With the Prometheus Operator, set `metrics.serviceMonitor.enabled: true` and add
the labels your Prometheus selects on under `metrics.serviceMonitor.labels`.

### Cloud provider credentials

Give the pod's ServiceAccount a cloud identity for AWS Bedrock or Google Vertex AI:

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/gomodel
```

## Values

| Key | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Pods. More than one needs `storage.type` postgresql or mongodb. |
| `image.repository` | `enterpilot/gomodel` | |
| `image.tag` | `""` | Defaults to the chart `appVersion`. |
| `env` | `{}` | Plain environment variables. |
| `secretEnv` | `{}` | Secret environment variables. |
| `extraEnvFrom` | `[]` | Existing Secrets/ConfigMaps loaded as env vars. |
| `extraEnv` | `[]` | Raw container env entries (`valueFrom`). |
| `config` | `""` | Inline `config.yaml`. |
| `existingConfigMap` | `""` | ConfigMap with a `config.yaml` key. |
| `storage.type` | `sqlite` | `sqlite`, `postgresql`, or `mongodb`. |
| `storage.url` | `""` | Connection URL for postgresql/mongodb. |
| `persistence.enabled` | `true` | PVC for SQLite data. |
| `persistence.size` | `1Gi` | |
| `persistence.storageClass` | `""` | Cluster default. `-` disables dynamic provisioning. |
| `persistence.existingClaim` | `""` | Reuse a PVC. |
| `updateStrategy` | `{}` | Overrides the automatic Recreate/RollingUpdate choice. |
| `metrics.enabled` | `true` | Expose `/metrics`. |
| `metrics.serviceMonitor.enabled` | `false` | Prometheus Operator ServiceMonitor. |
| `service.type` | `ClusterIP` | |
| `service.port` | `8080` | |
| `ingress.enabled` | `false` | |
| `httpRoute.enabled` | `false` | Gateway API HTTPRoute; needs `httpRoute.parentRefs`. |
| `serviceAccount.create` | `true` | |
| `serviceAccount.annotations` | `{}` | Cloud IAM bindings. |
| `resources` | `100m/128Mi` requests, `512Mi` limit | No CPU limit by default. |
| `autoscaling.enabled` | `false` | HPA on CPU (70%) and optionally memory. |
| `podDisruptionBudget.enabled` | `true` | Created only when more than one pod can run. |
| `terminationGracePeriodSeconds` | `45` | Covers GoModel's 30s drain. |
| `livenessProbe` / `readinessProbe` | `/health`, `/health/ready` | Readiness fails while storage is down. |
| `podSecurityContext` / `securityContext` | non-root 65532, read-only FS | |
| `extraVolumes` / `extraVolumeMounts` | `[]` | CA bundles, local model catalogs, … |
| `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`, `priorityClassName`, `podAnnotations`, `podLabels` | | Standard scheduling knobs. |

## Upgrading from chart 0.1.x

Chart 0.2.0 replaced the per-provider values and the Redis subchart with the
generic `env` / `secretEnv` maps:

| 0.1.x | 0.2.0 |
| --- | --- |
| `auth.masterKey` | `secretEnv.GOMODEL_MASTER_KEY` |
| `providers.openai.apiKey` | `secretEnv.OPENAI_API_KEY` (same pattern for every provider) |
| `providers.openai.baseUrl` | `env.OPENAI_BASE_URL` |
| `providers.existingSecret` | `extraEnvFrom: [{secretRef: {name: ...}}]` |
| `redis.enabled` / `cache.redis.url` | `env.REDIS_URL` pointing at your own Redis |
| `gateway.*` | `httpRoute.*` |
| `server.basePath` | `env.BASE_PATH` |

The chart refuses to render while any of the old top-level keys (`auth`,
`providers`, `redis`, `cache`, `gateway`, `server`, `logging`) are still present,
so an upgrade cannot silently drop the master key. The default `replicaCount`
dropped from 2 to 1 because SQLite is per pod, the pod now runs as the image's
UID 65532, and `/app/data` is on a PVC.

## Testing the chart

```bash
helm lint --strict ./helm -f helm/ci/default-values.yaml
helm template gomodel ./helm -f helm/ci/full-values.yaml | kubeconform -strict
helm install gomodel ./helm -n gomodel --create-namespace -f helm/ci/default-values.yaml --wait
helm test gomodel -n gomodel
```

The `ci/*-values.yaml` profiles are what the CI workflow lints, validates,
and installs on a kind cluster.
