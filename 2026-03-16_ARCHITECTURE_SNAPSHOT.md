# GOModel Architecture Snapshot

This document is a point-in-time architecture snapshot based on the code and runtime wiring present on March 16, 2026.

It is not a statement of the intended architectural direction. It is a snapshot of how the system is structured as of that date.

It focuses on:

- what is instantiated at boot
- how requests move through the gateway
- what data objects are passed between layers
- where `RequestSnapshot`, `WhiteBoxPrompt`, and `Workflow` are created and consumed

## 1. Boot And Dependency Wiring

```mermaid
flowchart TB
    Main["cmd/gomodel/main.go
- config.Load()
- register provider constructors
- optional Prometheus hooks
- app.New()
- app.Start()"]

    Cfg["config.LoadResult
- Config
- RawProviders"]

    Factory["providers.ProviderFactory
constructors:
- openai
- anthropic
- gemini
- groq
- xai
- ollama
also exposes passthrough enrichers"]

    App["internal/app.New(...)"]

    Main --> Cfg
    Main --> Factory
    Cfg --> App
    Factory --> App

    subgraph ProviderSubsystem["Provider subsystem: providers.Init(...)"]
        Resolve["resolveProviders(...)
merge YAML, env, resilience config"]

        ProviderInstances["Provider instances"]

        ModelCache["modelcache.Cache
local file or Redis"]

        Registry["providers.ModelRegistry
- configured providers
- in-memory model map
- provider type map
- cache-backed warm start
- background refresh"]

        ModelList["modeldata.Fetch(...)
optional model list URL
background metadata enrichment"]

        Router["providers.Router
implements:
- translated routing
- passthrough routing
- native batch routing
- native file routing"]

        Resolve --> ProviderInstances
        Factory --> ProviderInstances
        ProviderInstances -->|"RegisterProviderWithNameAndType"| Registry
        ModelCache <-->|"LoadFromCache / SaveToCache"| Registry
        ModelList -->|"SetModelList + EnrichModels"| Registry
        Registry --> Router
    end

    subgraph StorageSubsystem["Storage-backed subsystems"]
        Audit["auditlog.New(...)
-> Logger
-> optional Storage"]

        Usage["usage.New(...) or
usage.NewWithSharedStorage(...)
-> Logger
-> optional Storage"]

        Batch["batch.New(...) or
batch.NewWithSharedStorage(...)
-> Batch Store"]

        Aliases["aliases.New(...) or
aliases.NewWithSharedStorage(...)
-> aliases.Service"]

        Audit -->|"shared storage when available"| Usage
        Audit -->|"shared storage when available"| Batch
        Audit -->|"shared storage when available"| Aliases
        Usage -->|"fallback shared storage"| Batch
        Usage -->|"fallback shared storage"| Aliases
        Batch -->|"fallback shared storage"| Aliases
    end

    Registry -->|"catalog for alias validation
and provider-type lookup"| Aliases

    subgraph PolicySubsystem["Optional policy layer"]
        Guardrails["guardrails.Pipeline"]
        RequestPatcher["guardrails.RequestPatcher
translated request patcher"]
        BatchPreparers["ComposeBatchRequestPreparers(...)
- aliases batch rewrite
- optional guardrails batch rewrite"]

        Guardrails --> RequestPatcher
        Guardrails --> BatchPreparers
        Aliases --> BatchPreparers
    end

    subgraph AdminSubsystem["Optional admin layer"]
        AdminHandler["admin.Handler
usage, audit, models, aliases"]
        Dashboard["dashboard.Handler"]
        AdminHandler --> Dashboard
    end

    Router -->|"core.RoutableProvider"| Server["server.New(...)
Echo + Handler"]
    Audit -->|"AuditLogger"| Server
    Usage -->|"UsageLogger"| Server
    Registry -->|"PricingResolver"| Server
    Aliases -->|"ModelResolver
+ ExposedModelLister"| Server
    RequestPatcher --> Server
    BatchPreparers --> Server
    Factory -->|"PassthroughSemanticEnrichers"| Server
    Batch -->|"BatchStore"| Server
    AdminHandler --> Server
    Dashboard --> Server

    Server --> HTTP["HTTP surface
- /v1/*
- /p/*
- /admin/api/v1/*
- /admin/dashboard
- /metrics
- /health
- /swagger/*"]
```

## 2. Request-Scoped Data Objects

| Object | Created by | Contains | Consumed by |
| --- | --- | --- | --- |
| `RequestSnapshot` | `RequestSnapshotCapture()` | Immutable ingress transport data: method, path, route params, query params, headers, content type, captured body bytes, `BodyNotCaptured`, request id, trace metadata | `DeriveWhiteBoxPrompt`, audit logging, passthrough semantic enrichers, any later logic that needs raw ingress fidelity |
| `WhiteBoxPrompt` | `core.DeriveWhiteBoxPrompt(snapshot)` | Best-effort semantics: route type, operation type, route hints, stream intent, JSON parsed flag, cached typed request objects, cached route metadata | workflow resolution, canonical request decoding, passthrough/file/batch helpers |
| `Workflow` | `WorkflowResolutionWithResolver(...)` or `ensureTranslatedRequestWorkflow(...)` | Control-plane decision: endpoint descriptor, execution mode, capabilities, provider type, resolved model selector, passthrough info | response cache, translated handlers, passthrough handlers, audit-log enrichment |

Important constraints:

- `RequestSnapshot` is transport-first and must not be mutated.
- `WhiteBoxPrompt` is best-effort and may be partial or absent.
- `Workflow` is request-scoped control-plane state, not raw transport state.
- Streaming response frames are not part of `RequestSnapshot`.

## 3. Model-Facing Request Lifecycle

This pipeline applies to ingress-managed model routes such as:

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/embeddings`
- `/v1/batches*`
- `/v1/files*`
- `/p/:provider/*`

It does not apply to `/health`, `/metrics`, `/swagger/*`, admin UI assets, or `GET /v1/models`.

```mermaid
flowchart TB
    Client["Client
HTTP request"]

    subgraph EchoPipeline["Echo middleware order"]
        M0["RequestLogger
Recover
BodyLimit"]

        M1["Request ID middleware
ensure X-Request-ID
store request id in context
echo request id in response header"]

        M2["RequestSnapshotCapture"]

        M3["auditlog.Middleware
create LogEntry"]

        M4["AuthMiddleware
skips public paths"]

        M5["PassthroughSemanticEnrichment
provider-owned enrichers for /p/*"]

        M6["WorkflowResolutionWithResolver"]

        M7["ResponseCacheMiddleware
only POST:
- /v1/chat/completions
- /v1/responses
- /v1/embeddings"]

        H["Route handler"]
    end

    Client -->|"HTTP request"| M0 --> M1 --> M2 --> M3 --> M4 --> M5 --> M6 --> M7 --> H

    M2 -->|"construct"| Snapshot["core.RequestSnapshot
method
path
route params
query params
headers
content type
captured body bytes
BodyNotCaptured
request id
trace metadata"]

    Snapshot -->|"core.DeriveWhiteBoxPrompt(snapshot)"| WBP["core.WhiteBoxPrompt
RouteType
OperationType
RouteHints: model, provider, endpoint
StreamRequested
JSONBodyParsed
cache:
- ChatRequest
- ResponsesRequest
- EmbeddingRequest
- BatchRequest
- BatchRouteInfo
- FileRouteInfo
- PassthroughRouteInfo"]

    Snapshot -.->|"stored in request context"| M3
    Snapshot -.->|"stored in request context"| M5
    WBP -.->|"stored in request context"| M5
    WBP -.->|"selector hints / cached canonical request"| M6

    M5 -->|"enrich cached PassthroughRouteInfo
using RequestSnapshot + WhiteBoxPrompt"| WBP

    M6 -->|"build"| Plan["core.Workflow
RequestID
EndpointDescriptor
Mode:
- translated
- passthrough
- native_batch
- native_file
CapabilitySet
ProviderType
RequestModelResolution or PassthroughRouteInfo"]

    Plan -.->|"stored in request context"| M7
    Plan -.->|"used by audit-log enrichment"| M3
    Plan -.->|"consumed by handlers"| H

    M7 -->|"cache key:
path + raw body + plan.mode
+ plan.providerType + plan.resolvedModel"| CacheStore["Redis response cache
optional"]
```

## 4. Execution Branches After Routing

```mermaid
flowchart TB
    H["Resolved route handler"]

    H --> TStart["Translated endpoints
/v1/chat/completions
/v1/responses
/v1/embeddings"]

    H --> FStart["Native file endpoints
/v1/files*"]

    H --> BStart["Native batch endpoints
/v1/batches*"]

    H --> PStart["Provider passthrough
/p/:provider/*"]

    subgraph Translated["Translated OpenAI-compatible execution"]
        T1["canonicalJSONRequestFromSemantics
RequestSnapshot body + WhiteBoxPrompt
-> ChatRequest or ResponsesRequest or EmbeddingRequest"]

        T2["ensureTranslatedRequestWorkflow
selector hints from WhiteBoxPrompt
-> RequestModelResolution
requested selector -> resolved selector"]

        T3["TranslatedRequestPatcher
optional guardrails patching
typed request -> patched typed request"]

        T4["providers.Router
resolveProvider(...)
forward*Request(...)
rewrite model to concrete upstream model
clear provider field before upstream call"]

        T5["Concrete provider adapter
OpenAI
Anthropic
Gemini
Groq
xAI
Ollama"]

        T6["Synchronous OpenAI-compatible JSON response
provider field stamped on response"]

        T7["Streaming response
io.ReadCloser"]

        T8["auditlog.WrapStreamForLogging"]

        T9["usage.WrapStreamForUsage"]
    end

    TStart --> T1 --> T2 --> T3 --> T4 --> T5
    T5 -->|"non-stream"| T6
    T5 -->|"SSE stream"| T7 --> T8 --> T9

    subgraph Files["Native file execution"]
        F1["fileRouteInfoFromSemantics
-> FileRouteInfo
provider, purpose, file id, limit, filename"]

        F2["nativeFileService
choose provider from:
- ?provider query
- single configured file provider
- multi-provider inventory scan"]

        F3["core.NativeFileRoutableProvider"]

        F4["Provider file API
create
list
get
delete
content"]
    end

    FStart --> F1 --> F2 --> F3 --> F4

    subgraph Batches["Native batch execution"]
        B1["DecodeBatchRequest
+ BatchRouteInfo"]

        B2["determineBatchProviderType
from request endpoint, model, aliases"]

        B3["BatchRequestPreparer chain
- aliases batch rewrite
- optional guardrails batch rewrite"]

        B4["core.NativeBatchRoutableProvider"]

        B5["BatchStore persistence
gateway batch id
provider batch id
request endpoint hints
rewritten input file ids
request id"]

        B6["Batch results usage extraction
when results are fetched"]
    end

    BStart --> B1 --> B2 --> B3 --> B4 --> B5 --> B6

    subgraph Passthrough["Opaque provider passthrough execution"]
        P1["passthroughExecutionTarget
-> provider type
-> normalized endpoint
-> PassthroughRouteInfo"]

        P2["core.PassthroughRequest
Method
Endpoint
Body
Headers
request id propagated"]

        P3["providers.Router.resolvePassthroughProvider"]

        P4["Provider adapter.Passthrough(...)"]

        P5["Opaque upstream response
JSON or binary or SSE
upstream status code preserved"]
    end

    PStart --> P1 --> P2 --> P3 --> P4 --> P5

    T6 --> Client["Client response"]
    T9 --> Client
    F4 --> Client
    B6 --> Client
    P5 --> Client
```

## 5. What Is Passed Where

Translated request path:

1. HTTP ingress data becomes `RequestSnapshot`.
2. `RequestSnapshot` becomes `WhiteBoxPrompt`.
3. `WhiteBoxPrompt` plus request body decoding becomes a typed request such as `*core.ChatRequest`.
4. `WhiteBoxPrompt` selector hints plus alias resolution become `RequestModelResolution`.
5. `RequestModelResolution` becomes part of `Workflow`.
6. `Workflow` drives:
   - response-cache keying
   - provider selection
   - audit-log enrichment
   - usage attribution
7. `providers.Router` rewrites the outgoing request to the concrete upstream model and clears the provider field before invoking the provider adapter.
8. The provider adapter returns either:
   - a typed OpenAI-compatible response object
   - an `io.ReadCloser` SSE stream

Passthrough request path:

1. HTTP ingress data becomes `RequestSnapshot`.
2. `RequestSnapshot` becomes `WhiteBoxPrompt`.
3. Provider-owned passthrough enrichment can add `PassthroughRouteInfo` such as normalized endpoint, semantic operation, or model hints.
4. `Workflow` is created in `passthrough` mode with `ProviderType` and `PassthroughRouteInfo`.
5. The handler converts the live request into `*core.PassthroughRequest`:
   - `Method`
   - normalized `Endpoint`
   - live `Body`
   - forwarded `Headers`
6. The selected provider adapter executes the opaque upstream request and the gateway proxies the upstream response back to the client.

Batch request path:

1. The request body is decoded into `*core.BatchRequest`.
2. Batch provider type is determined from request semantics plus alias policy.
3. The batch preparer chain can rewrite input files or per-item request payloads.
4. The native batch router sends the prepared request to the selected provider.
5. The gateway persists its own batch id plus provider ids and request-endpoint hints in `BatchStore`.

File request path:

1. Transport and multipart/query/path data become `FileRouteInfo`.
2. `nativeFileService` resolves the provider from query parameters or provider inventory.
3. The native file router forwards to the selected provider file API.

## 6. Side Paths Outside The Main Ingress Pipeline

- `GET /v1/models`: `Handler.ListModels` calls `providers.Router.ListModels()` and then merges alias-exposed models from `aliases.Service`.
- `/admin/api/v1/*`: reads usage and audit storage, model registry, and alias service through `admin.Handler`.
- `/admin/dashboard`: dashboard UI handler over the same underlying readers.
- `/metrics`: Prometheus endpoint when enabled.
- `/health`: simple health check.
- `/swagger/*`: Swagger UI when enabled.
