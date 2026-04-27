# GoModel Helm Chart

High-performance AI gateway for multiple LLM providers (OpenAI, Anthropic, Gemini, DeepSeek, Groq, Z.ai, xAI, Oracle).

## Prerequisites

- Kubernetes 1.29+ (for Gateway API v1 support)
- Helm 3.x
- (Optional) Prometheus Operator for ServiceMonitor support

## Installation

### Add the Helm repository (if published)

```bash
helm repo add gomodel https://your-org.github.io/gomodel
helm repo update
```

### Install from local chart

```bash
# Basic install with OpenAI (provider auto-enables when apiKey is set)
helm install gomodel ./helm \
  -n gomodel --create-namespace \
  --set providers.openai.apiKey="sk-..."

# Multi-provider setup with Redis cache
helm install gomodel ./helm \
  -n gomodel --create-namespace \
  --set providers.openai.apiKey="sk-..." \
  --set providers.anthropic.apiKey="sk-ant-..." \
  --set redis.enabled=true

# Using existing secrets (GitOps-friendly)
helm install gomodel ./helm \
  -n gomodel --create-namespace \
  --set providers.existingSecret="llm-api-keys" \
  --set providers.openai.enabled=true \
  --set providers.anthropic.enabled=true
```

## Configuration

### Key Values

| Parameter                        | Description                                                                                    | Default                |
| -------------------------------- | ---------------------------------------------------------------------------------------------- | ---------------------- |
| `replicaCount`                   | Number of replicas                                                                             | `2`                    |
| `image.repository`               | Image repository                                                                               | `enterpilot/gomodel`   |
| `image.tag`                      | Image tag                                                                                      | `""` (uses appVersion) |
| `server.port`                    | Server port                                                                                    | `8080`                 |
| `server.basePath`                | URL path prefix where GoModel is mounted                                                       | `"/"`                  |
| `server.bodySizeLimit`           | Max request body size                                                                          | `"10M"`                |
| `auth.masterKey`                 | Master key for auth                                                                            | `""`                   |
| `auth.existingSecret`            | Existing secret for auth                                                                       | `""`                   |
| `providers.existingSecret`       | Existing secret for API keys                                                                   | `""`                   |
| `providers.openai.enabled`       | Enable OpenAI                                                                                  | `false`                |
| `providers.anthropic.enabled`    | Enable Anthropic                                                                               | `false`                |
| `providers.gemini.enabled`       | Enable Gemini                                                                                  | `false`                |
| `providers.groq.enabled`         | Enable Groq                                                                                    | `false`                |
| `providers.xai.enabled`          | Enable xAI                                                                                     | `false`                |
| `providers.zai.enabled`          | Enable Z.ai                                                                                    | `false`                |
| `providers.zai.baseUrl`          | Optional Z.ai base URL mapped to `ZAI_BASE_URL`; use Coding Plan endpoint when needed          | `""`                   |
| `providers.oracle.enabled`       | Enable Oracle                                                                                  | `false`                |
| `providers.oracle.baseUrl`       | Oracle OpenAI-compatible base URL mapped to `ORACLE_BASE_URL`; required when Oracle is enabled | `""`                   |
| `providers.vllm.enabled`         | Enable vLLM                                                                                    | `false`                |
| `providers.vllm.baseUrl`         | vLLM OpenAI-compatible base URL mapped to `VLLM_BASE_URL`; required when vLLM is enabled       | `""`                   |
| `cache.type`                     | Cache type (local/redis)                                                                       | `"redis"`              |
| `redis.enabled`                  | Deploy Redis subchart                                                                          | `true`                 |
| `metrics.enabled`                | Enable Prometheus metrics                                                                      | `true`                 |
| `metrics.serviceMonitor.enabled` | Create ServiceMonitor                                                                          | `false`                |
| `logging.format`                 | Log format; empty auto-detects, or set `json`/`text`                                           | `""`                   |
| `ingress.enabled`                | Enable Ingress                                                                                 | `false`                |
| `gateway.enabled`                | Enable Gateway API HTTPRoute                                                                   | `false`                |
| `autoscaling.enabled`            | Enable HPA                                                                                     | `false`                |

### Using Existing Secrets

Create a secret with your API keys:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-api-keys
type: Opaque
stringData:
  OPENAI_API_KEY: "sk-..."
  ANTHROPIC_API_KEY: "sk-ant-..."
  GEMINI_API_KEY: "..."
  ZAI_API_KEY: "..."
  ORACLE_API_KEY: "..."
  VLLM_API_KEY: "..."
```

Oracle also requires a base URL in values. The chart maps `providers.oracle.baseUrl`
to the container env var `ORACLE_BASE_URL`.

vLLM does not require an API key unless the upstream server was started with
`--api-key`. The chart maps `providers.vllm.baseUrl` to the container env var
`VLLM_BASE_URL`.

Then reference it (use `enabled=true` when using existingSecret since apiKey isn't set directly):

```bash
helm install gomodel ./helm \
  --set providers.existingSecret="llm-api-keys" \
  --set providers.openai.enabled=true
```

Example Oracle setup with an existing secret:

```bash
helm install gomodel ./helm \
  --set providers.existingSecret="llm-api-keys" \
  --set providers.oracle.enabled=true \
  --set providers.oracle.baseUrl="https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1"
```

Example keyless vLLM setup:

```bash
helm install gomodel ./helm \
  --set providers.vllm.enabled=true \
  --set providers.vllm.baseUrl="http://vllm.default.svc.cluster.local:8000/v1"
```

### Ingress Example

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: gomodel.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: gomodel-tls
      hosts:
        - gomodel.example.com
```

### Gateway API Example

```yaml
gateway:
  enabled: true
  parentRef:
    name: my-gateway
    namespace: gateway-system
  hostnames:
    - gomodel.example.com
```

## Upgrading

```bash
helm upgrade gomodel ./helm -n gomodel -f values.yaml
```

## Uninstalling

```bash
helm uninstall gomodel -n gomodel
```

# Todo

- Add a values-demo.yaml file with a demo setup ready to run
- Consider adding prometheus + grafana stack as an optional subchart
- Add an example for production-ready redis configuration with persistence and authentication enabled
