# Local Kubernetes Development

Develop GoModel against a local [kind](https://kind.sigs.k8s.io/) cluster using
[Skaffold](https://skaffold.dev/) for a build → deploy → watch loop. The app is
deployed through the same production [Helm chart](../../deploy/helm/gomodel) with dev overrides,
so you exercise the real Kubernetes, storage and cache code paths locally.

## Prerequisites

Install these once:

| Tool | Purpose | Install |
| --- | --- | --- |
| [Docker](https://docs.docker.com/get-docker/) | Container runtime for kind + image builds | — |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | Local Kubernetes cluster | `go install sigs.k8s.io/kind@latest` |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Cluster CLI | — |
| [Helm](https://helm.sh/docs/intro/install/) | Chart rendering | — |
| [Skaffold](https://skaffold.dev/docs/install/) | Build + deploy inner loop | — |

## Layout

```
deploy/local/
  kind-cluster.yaml   # kind cluster (maps NodePort 30080 -> host 8080)
  deps.yaml           # in-cluster Redis + PostgreSQL + MongoDB (ephemeral)
  mockllm.yaml        # in-cluster OpenAI-compatible mock upstream provider
  values.yaml         # Helm dev overrides (stateless: Postgres + Redis)
skaffold.yaml         # build (Dockerfile.dev) + deploy (Helm)
Dockerfile.dev        # fast native-arch build image
```

The dependencies (Redis, PostgreSQL, MongoDB) and the mock upstream provider are
deployed once by `make kind-up` and persist across app redeploys — Skaffold
manages only the GoModel app. This guarantees the datastores are ready before the
app's first boot.

## Mock upstream provider

`deploy/local/mockllm.yaml` runs a tiny stdlib-only Python server
([`ThreadingHTTPServer`](https://docs.python.org/3/library/http.server.html))
mounted from a ConfigMap into a `python:3.13-alpine` pod — no second image build,
fully offline. It speaks the slice of the OpenAI API that GoModel needs:

- `GET /v1/models` — advertises `gpt-4o`, `gpt-4o-mini`, `gpt-3.5-turbo`.
- `POST /v1/chat/completions` — streaming (SSE) and non-streaming.
- `POST /v1/responses` — streaming and non-streaming.

It accepts **any** API key, so GoModel routes real requests end-to-end without
provider credentials. The dev values point the OpenAI provider at it via
`OPENAI_BASE_URL: http://mockllm:8080/v1`, which is how the gateway loads three
models on boot as `openai/gpt-4o`, `openai/gpt-4o-mini`, `openai/gpt-3.5-turbo`.

To point at a real provider instead, drop `OPENAI_BASE_URL` from
`deploy/local/values.yaml` and set a real `OPENAI_API_KEY` (see
[Adding a provider API key](#adding-a-provider-api-key)).

## Quick start

```bash
make kind-up     # create the kind cluster + deploy deps + mock LLM (one time)
make dev-k8s     # build, load into kind, deploy, watch for changes
```

Then, in another terminal:

```bash
curl localhost:8080/health          # {"status":"ok"}
curl localhost:8080/health/ready     # {"status":"ready", ...} once Postgres is reachable
open http://localhost:8080/admin/dashboard
```

List the models loaded from the mock upstream, then send a chat completion
straight through the gateway (auth uses the dev master key):

```bash
curl localhost:8080/v1/models -H "Authorization: Bearer dev-master-key"
# lists openai/gpt-4o, openai/gpt-4o-mini, openai/gpt-3.5-turbo

curl localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer dev-master-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-3.5-turbo","messages":[{"role":"user","content":"ping"}]}'
# {"...","choices":[{"message":{"role":"assistant","content":"Mock response to: ping"}...
```

Add `"stream":true,"stream_options":{"include_usage":true}` for a streamed SSE
response.

Edit any Go file and Skaffold automatically rebuilds the image, reloads it into
kind, and rolls the pod. Press `Ctrl+C` to stop `skaffold dev` (it uninstalls the
app release; the dependencies stay up).

## Common commands

```bash
make deploy-k8s     # one-shot build + deploy (no watch)
make undeploy-k8s   # remove the app release (dependencies stay up)
make kind-down      # delete the whole cluster (removes dependencies too)
```

## Configuration

Dev overrides live in [`deploy/local/values.yaml`](../../deploy/local/values.yaml):

- **Stateless mode** backed by the in-cluster `postgres` and `redis` Services.
- `image.pullPolicy: IfNotPresent` — the image is built locally and loaded into
  kind (never pulled from a registry). Skaffold rewrites `image.repository`/`tag`.
- `secrets.masterKey: dev-master-key` — **dev only**, never reuse elsewhere.
- `secrets.data.OPENAI_API_KEY: sk-mock` and `env.OPENAI_BASE_URL:
  http://mockllm:8080/v1` — point the OpenAI provider at the in-cluster
  [mock upstream](#mock-upstream-provider) so the gateway boots with real models.
- Verbose logging (`LOG_LEVEL=debug`, text format) and audit logging enabled.

### Adding a provider API key

Replace the placeholder or add credentials under `secrets.data` in
`deploy/local/values.yaml`, and drop `env.OPENAI_BASE_URL` to hit the real
upstream instead of the mock:

```yaml
secrets:
  data:
    OPENAI_API_KEY: "sk-..."
```

Skaffold redeploys on save. Avoid committing real keys — keep them in your local
working copy only.

### Switching to SQLite

For the lightest footprint, deploy in SQLite mode instead of Postgres/Redis:
set `persistence.enabled: true` and remove the `storage`/`cache` blocks from
`deploy/local/values.yaml`. The chart then runs a single-replica StatefulSet
with a PVC at `/app/data`.

## Access

The gateway is reachable at `http://localhost:8080` via the **kind NodePort
mapping**: the Service is a `NodePort` on `30080`, which
`deploy/local/kind-cluster.yaml` maps to host port `8080`. This works whether or
not `skaffold dev` is running (e.g. after `make deploy-k8s`), so no separate
port-forward is needed.

## Troubleshooting

- **`ErrImageNeverPull` / image not found** — ensure you launched via Skaffold
  (`make dev-k8s`/`deploy-k8s`) so the image is loaded into kind. `pullPolicy`
  must be `IfNotPresent` (already set in dev values).
- **App CrashLoopBackOff: `no providers were successfully registered`** — at
  least one `<PROVIDER>_API_KEY` must be set. The dev values ship
  `OPENAI_API_KEY: sk-mock` pointed at the in-cluster mock; keep it or add your own.
- **Gateway `/v1/models` empty / registry `failed_providers`** — the mock upstream
  isn't reachable. Ensure `make kind-up` deployed it (`kubectl get pods` shows
  `mockllm`) and that `env.OPENAI_BASE_URL` is `http://mockllm:8080/v1`.
- **App CrashLoopBackOff: `failed to connect to redis` / DNS `server
  misbehaving`** — the dependencies aren't up yet. Run `make kind-up` (it applies
  `deploy/local/deps.yaml` and waits for rollout) before deploying the app.
- **Readiness stuck at 503** — the app returns `503` from `/health/ready` until
  primary storage (Postgres) is reachable. Check `kubectl get pods` and
  `kubectl logs deploy/postgres`.
- **MongoDB not ready** — the replica set self-initializes via the readiness
  probe on first boot; give it up to ~30s.
- **Port 8080 already in use** — stop the conflicting process, or change the
  `hostPort` in `deploy/local/kind-cluster.yaml` (then recreate the cluster).
