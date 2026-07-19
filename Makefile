.PHONY: all build run demo clean tidy test test-race test-dashboard test-e2e test-integration test-contract test-all lint lint-fix fix fix-check record-api swagger docs-openapi install-tools perf-check perf-bench infra image seed-demo-data kind-up kind-down dev-k8s deploy-k8s undeploy-k8s

all: build

# Get version info
VERSION ?= $(shell git describe --tags --always --dirty)
COMMIT ?= $(shell git rev-parse --short HEAD)
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
DOCS_API_SERVERS ?= http://localhost:8080
LOG_LEVEL ?= debug
SWAGGER_ENABLED ?= true

# Build tags covering every file the linter and fixers must see. Without these,
# tag-gated files (tests/e2e, tests/integration, tests/contract) are skipped.
BUILD_TAGS ?= swagger,e2e,integration,contract

# Linker flags to inject version info
LDFLAGS := -X "github.com/enterpilot/gomodel/internal/version.Version=$(VERSION)" \
           -X "github.com/enterpilot/gomodel/internal/version.Commit=$(COMMIT)" \
           -X "github.com/enterpilot/gomodel/internal/version.Date=$(DATE)"

install-tools:
	@command -v golangci-lint > /dev/null 2>&1 || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10)
	@command -v pre-commit > /dev/null 2>&1 || (echo "Installing pre-commit..." && pip install pre-commit==4.5.1)
	@echo "All tools are ready"

build:
	go build -ldflags '$(LDFLAGS)' -o bin/gomodel ./cmd/gomodel
# Run the application
run:
	LOG_LEVEL=$(LOG_LEVEL) SWAGGER_ENABLED=$(SWAGGER_ENABLED) go run -tags=swagger ./cmd/gomodel

# Seed the local SQLite database and start GoModel with a populated dashboard.
demo: seed-demo-data
	$(MAKE) run

# Clean build artifacts
clean:
	rm -rf bin/

# Tidy dependencies
tidy:
	go mod tidy

# Docker Compose: Redis, PostgreSQL, MongoDB, Adminer (no app image build)
infra:
	docker compose up -d

# Docker Compose: full stack (GoModel + Prometheus; builds app image when needed)
image:
	docker compose --profile app up -d

# Seed rolling demo telemetry and dashboard configuration into SQLite.
# Usage: SQLITE_PATH=data/gomodel.db make seed-demo-data
seed-demo-data:
	bash tools/seed-demo-data.sh

# ---------------------------------------------------------------------------
# Local Kubernetes development (kind + Skaffold)
# See docs/dev/local-kubernetes.md
# ---------------------------------------------------------------------------
KIND_CLUSTER ?= gomodel-dev

# Create the local kind cluster and deploy in-cluster dependencies (idempotent).
kind-up:
	@kind get clusters | grep -qx "$(KIND_CLUSTER)" \
		|| kind create cluster --config deploy/local/kind-cluster.yaml
	@kubectl cluster-info --context kind-$(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/local/deps.yaml
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/local/mockllm.yaml
	kubectl --context kind-$(KIND_CLUSTER) rollout status deploy/redis --timeout=120s
	kubectl --context kind-$(KIND_CLUSTER) rollout status deploy/postgres --timeout=120s
	kubectl --context kind-$(KIND_CLUSTER) rollout status deploy/mongodb --timeout=180s
	kubectl --context kind-$(KIND_CLUSTER) rollout status deploy/mockllm --timeout=120s

# Delete the local kind cluster.
kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

# Inner dev loop: build image, load into kind, deploy via Helm, watch for changes.
# The gateway is reachable at http://localhost:8080 via the kind NodePort mapping.
dev-k8s:
	skaffold dev --kube-context kind-$(KIND_CLUSTER)

# One-shot build + deploy (no watch).
deploy-k8s:
	skaffold run --kube-context kind-$(KIND_CLUSTER)

# Tear down the deployed release and dependencies.
undeploy-k8s:
	skaffold delete --kube-context kind-$(KIND_CLUSTER)

# Run unit tests only
test:
	go test ./cmd/... ./internal/... ./config/... -v

# Run unit tests with race detection and coverage
test-race:
	go test -v -race -coverprofile=coverage.out ./cmd/... ./internal/... ./config/...

# Run dashboard JavaScript unit tests
test-dashboard:
	node --test internal/admin/dashboard/static/js/modules/*.test.cjs

# Run e2e tests (uses an in-process mock LLM server; no Docker required)
test-e2e:
	go test -v -tags=e2e ./tests/e2e/...

# Run integration tests (requires Docker)
test-integration:
	go test -v -tags=integration -timeout=10m ./tests/integration/...

# Run contract tests (validates API response structures against golden files)
test-contract:
	go test -v -tags=contract -timeout=5m ./tests/contract/...

# Run all tests including dashboard, e2e, integration, and contract tests
test-all: test test-dashboard test-e2e test-integration test-contract

perf-check:
	go test -run '^TestHotPathPerfGuard$$' -count=1 -v ./tests/perf/...

perf-bench:
	go test -bench=. -benchmem ./tests/perf/...

# Record API responses for contract tests
# Usage: OPENAI_API_KEY=sk-xxx make record-api
record-api:
	@echo "Recording OpenAI chat completion..."
	go run ./cmd/recordapi -provider=openai -endpoint=chat \
		-output=tests/contract/testdata/openai/chat_completion.json
	@echo "Recording OpenAI models..."
	go run ./cmd/recordapi -provider=openai -endpoint=models \
		-output=tests/contract/testdata/openai/models.json
	@echo "Done! Golden files saved to tests/contract/testdata/"

swagger:
	go run github.com/swaggo/swag/v2/cmd/swag init --generalInfo main.go \
		--dir cmd/gomodel,internal \
		--output cmd/gomodel/docs \
		--outputTypes go \
		--parseDependency
	@command -v node >/dev/null 2>&1 || { echo "node is required to build docs; install from https://nodejs.org" >&2; exit 1; }
	node tools/swagger-postprocess.mjs cmd/gomodel/docs/docs.go
	$(MAKE) docs-openapi

docs-openapi:
	@command -v node >/dev/null 2>&1 || { echo "node is required to build docs; install from https://nodejs.org" >&2; exit 1; }
	@command -v npx >/dev/null 2>&1 || { echo "npx is required; install npm (includes npx)" >&2; exit 1; }
	@tmp_dir=$$(mktemp -d); \
	trap 'rm -rf "$$tmp_dir"' EXIT; \
	go run github.com/swaggo/swag/v2/cmd/swag init --quiet --generalInfo main.go \
		--dir cmd/gomodel,internal \
		--output "$$tmp_dir" \
		--outputTypes json \
		--parseDependency; \
	npx -y swagger2openapi@7.0.8 --patch -o docs/openapi.json "$$tmp_dir/swagger.json"; \
	DOCS_API_SERVERS="$(DOCS_API_SERVERS)" node tools/openapi-postprocess.mjs docs/openapi.json

# Run linter
lint:
	golangci-lint run --build-tags=$(BUILD_TAGS) ./cmd/... ./config/... ./internal/... ./tests/...

# Run linter with auto-fix. Mirrors `lint`: same tags, same packages, so the
# autofix pass cannot silently skip the tag-gated files under tests/.
lint-fix:
	golangci-lint run --fix --build-tags=$(BUILD_TAGS) ./cmd/... ./config/... ./internal/... ./tests/...

# Report modernizations go fix would apply, without touching the tree.
# Exits non-zero when the tree has drifted; run `make fix` to apply.
fix-check:
	go fix -diff -tags=$(BUILD_TAGS) ./...

# Apply go fix modernizations in place.
#
# Not idempotent in one pass: when go fix marks a helper `//go:fix inline` it
# inlines the callers on the *next* run, which can leave the helper orphaned.
# Re-run until `make fix-check` is clean, then delete any helper `make lint`
# now reports as unused.
fix:
	go fix -tags=$(BUILD_TAGS) ./...
