## `.` (Root Directory)

It contains the repository configuration, build files, and dependency definitions.

- **`.dockerignore`**: Excludes unnecessary files from the Docker build context.
- **`.env.template`**: Template containing all environment variable configurations supported by the application.
- **`.gitignore`**: Defines files and directories to be ignored by Git.
- **`.golangci.yml`**: Configuration for the golangci-lint static analysis tool.
- **`.goreleaser.yaml`**: Configuration for GoReleaser to automate application builds, packaging, and releases.
- **`.pre-commit-config.yaml`**: Defines pre-commit hooks for code formatting, linting, and performance checks.
- **`docker-compose.yaml`**: Orchestration file to run GoModel alongside Redis, PostgreSQL, and MongoDB locally.
- **`Dockerfile`**: Multi-stage build instructions to compile and package the GoModel binary into a distroless container.
- **`go.mod` / `go.sum`**: Go module dependencies and checksums.
- **`LICENSE`**: MIT License file.
- **`Makefile`**: Provides CLI commands for building, testing, linting, and running the application.
- **`prometheus.yml`**: Configuration for Prometheus metric scraping.

---

## `./cmd/`

It's responsible for the main entry points of the applications and CLI tools.

### `./cmd/gomodel/main.go`

It's the primary entry point for the GoModel API gateway.

- `lifecycleApp`: Interface defining `Start` and `Shutdown` methods.
- `shutdownApplication()`: Triggers and coordinates the graceful shutdown of the application.
- `startApplication()`: Starts the application and handles immediate startup failures.
- `main()`: Loads configurations, initializes logging, sets up providers (OpenAI, Anthropic, Gemini, etc.), starts the HTTP server, and listens for OS signals.

### `./cmd/gomodel/logging.go`

It configures the global `slog` instance for the application.

- `configureLogging()`: Sets up the default logger based on the environment configuration.
- `newLogHandler()`: Instantiates either a JSON or colored text log handler based on TTY presence and config.
- `parseLogLevel()`: Parses string log levels (e.g., "debug", "info") into `slog.Level`.

### `./cmd/recordapi/main.go`

It's a CLI utility used to record real API responses from AI providers to generate golden files for contract tests.

- `providerConfigs` / `endpointConfigs`: Static maps mapping provider requirements and endpoint payloads.
- `endpointRequiresResponsesCapability()`, `providerSupportsResponses()`: Capability checking for the `/v1/responses` endpoint.
- `main()`: Executes the HTTP request to the external provider and writes the output.
- `adjustForAnthropic()`: Mutates OpenAI payloads to fit Anthropic's payload expectations.
- `writeOutput()`, `writeStreamOutput()`: Helper functions to write JSON responses to the disk.

---

## `./config/`

It's responsible for defining, parsing, and validating the application's configuration from YAML and environment variables.

### `./config/config.go`

It defines all configuration structures and the loading mechanism.

- `Config`: The root configuration structure holding all module sub-configs.
- `LoadResult`: Wrapper holding the parsed `Config` and the raw provider mappings.
- `RawProviderConfig`, `RawResilienceConfig`, `RawCircuitBreakerConfig`, `RawRetryConfig`: Structs mapping to the YAML provider definitions.
- `FallbackMode`: Typed string for fallback behaviors (`auto`, `manual`, `off`).
- `FallbackModelOverride`, `ModelsConfig`, `FallbackConfig`, `AdminConfig`, `GuardrailsConfig`, `GuardrailRuleConfig`, `SystemPromptSettings`, `LLMBasedAlteringSettings`, `HTTPConfig`, `ExecutionPlansConfig`, `LogConfig`, `UsageConfig`, `StorageConfig`, `SQLiteStorageConfig`, `PostgreSQLStorageConfig`, `MongoDBStorageConfig`, `CacheConfig`, `ModelCacheConfig`, `LocalCacheConfig`, `ModelListConfig`, `RedisModelConfig`, `RedisResponseConfig`, `ResponseCacheConfig`, `SimpleCacheConfig`, `SemanticCacheConfig`, `EmbedderConfig`, `VectorStoreConfig`, `QdrantConfig`, `PGVectorConfig`, `PineconeConfig`, `WeaviateConfig`, `ServerConfig`, `MetricsConfig`, `RetryConfig`, `CircuitBreakerConfig`, `ResilienceConfig`: Configuration domain structures.
- `ResolveFallbackDefaultMode()`: Normalizes the fallback mode.
- `ValidateCacheConfig()`, `SimpleCacheEnabled()`, `SemanticCacheActive()`: Validation and state checks for caching configurations.
- `applyResponseSimpleEnv()`, `applyResponseSemanticEnv()`: Overrides cache settings from environment variables.
- `buildDefaultConfig()`: Returns a `Config` initialized with default values.
- `Load()`: Loads `config.yaml` and applies environment variable overrides.
- `applyYAML()`: Reads and unmarshals the YAML file.
- `loadFallbackConfig()`: Loads the external JSON file defining manual fallback rules.
- `applyEnvOverrides()`, `hasEnvDescendants()`, `applyEnvOverridesValue()`: Reflection-based environment variable injection mapped by `env` tags.
- `expandString()`, `parseBool()`: Utility parsers.
- `ValidateBodySizeLimit()`, `ParseBodySizeLimitBytes()`: Parses strings like "10M" into byte integers.

---

## `./internal/admin/`

It's responsible for the Admin REST API logic and dashboard data provisioning.

### `./internal/admin/handler.go`

It maps HTTP routes to the underlying administrative services (Usage, Audit, Models, Guardrails, Aliases).

- `Handler`: The main admin controller struct holding references to readers and services.
- `Option`: Functional option type for configuring the `Handler`.
- `DashboardConfigResponse`: Struct defining the runtime configuration returned to the UI.
- `WithAuditReader()`, `WithAliases()`, `WithAuthKeys()`, `WithModelOverrides()`, `WithExecutionPlans()`, `WithGuardrailsRegistry()`, `WithGuardrailService()`, `WithDashboardRuntimeConfig()`: Configurator functions.
- `NewHandler()`: Initializes the admin API controller.
- `normalizeDashboardRuntimeConfig()`, `cloneDashboardRuntimeConfig()`: Config helpers.
- `parseUsageParams()`, `normalizeUserPathQueryParam()`, `parseDateRangeParams()`, `dashboardTimeZone()`: Extracts and formats dashboard query parameters.
- `handleError()`: Converts gateway errors into HTTP JSON responses.
- `UsageSummary()`, `usageSliceResponse()`, `DailyUsage()`, `UsageByModel()`, `UsageLog()`, `CacheOverview()`: API endpoints for analytics.
- `AuditLog()`, `AuditConversation()`: API endpoints for audit logs.
- `modelAccessResponse`, `modelInventoryResponse`: Structs for model catalog representation.
- `ListModels()`, `ListCategories()`, `DashboardConfig()`: Endpoints for model management state.
- `upsertAliasRequest`, `upsertModelOverrideRequest`, `upsertGuardrailRequest`, `createExecutionPlanRequest`, `createAuthKeyRequest`: Request body structures.
- `featureUnavailableError()`, `aliasesUnavailableError()`, `modelOverridesUnavailableError()`, `authKeysUnavailableError()`, `guardrailsUnavailableError()`, `executionPlansUnavailableError()`: Feature gating errors.
- `aliasWriteError()`, `modelOverrideWriteError()`, `executionPlanWriteError()`, `authKeyWriteError()`, `guardrailWriteError()`: Error formatting helpers.
- `deactivateByID()`, `deleteByName()`: Generic deletion HTTP handlers.
- `ListModelOverrides()`, `UpsertModelOverride()`, `DeleteModelOverride()`: Model override management endpoints.
- `ListAuthKeys()`, `CreateAuthKey()`, `DeactivateAuthKey()`: API key management endpoints.
- `ListAliases()`, `UpsertAlias()`, `DeleteAlias()`: Alias management endpoints.
- `ListGuardrailTypes()`, `ListGuardrails()`, `UpsertGuardrail()`, `DeleteGuardrail()`: Guardrail management endpoints.
- `ListExecutionPlans()`, `GetExecutionPlan()`, `ListExecutionPlanGuardrails()`, `CreateExecutionPlan()`, `DeactivateExecutionPlan()`: Workflow execution plan management endpoints.
- `refreshExecutionPlansAfterGuardrailChange()`, `activeWorkflowGuardrailReferences()`, `validateExecutionPlanGuardrails()`, `validateExecutionPlanScope()`: Business logic validators.
- `decodeAliasPathName()`, `decodeModelOverridePathSelector()`: Decodes URL-encoded identifiers.

---

## `./internal/admin/dashboard/`

It's responsible for compiling and serving the embedded web dashboard UI.

### `./internal/admin/dashboard/dashboard.go`

It loads and serves the HTML templates and static web assets.

- `Handler`: Struct holding the parsed `html/template` and the HTTP static file server.
- `New()`: Loads embedded files and generates asset hashes.
- `Index()`: Serves the `index.html` layout.
- `Static()`: Serves CSS, JS, and SVG files.
- `buildAssetVersions()`, `assetURL()`: Appends cache-busting `?v=hash` query parameters to static asset URLs.

### `./internal/admin/dashboard/static/js/dashboard.js`

It initializes the Alpine.js frontend application.

- `dashboard()`: The main Alpine.js data object. It merges state, handles URL routing (`_parseRoute`, `_applyRoute`, `navigate`), handles UI themes (`applyTheme`, `setTheme`), and fetches global data (`fetchAll`).

### `./internal/admin/dashboard/static/js/modules/`

Contains modular frontend business logic for the Alpine.js UI.

- `aliases.js`: `dashboardAliasesModule()` handles filtering, listing, and saving model aliases and model access overrides.
- `audit-list.js`: `dashboardAuditListModule()` handles fetching, filtering, and displaying audit logs and request/response JSON viewer panes.
- `auth-keys.js`: `dashboardAuthKeysModule()` handles creating, displaying, copying, and deactivating managed API keys.
- `charts.js`: `dashboardChartsModule()` manages Chart.js instance rendering for the usage over time and model bar charts.
- `clipboard.js`: `fallbackClipboardModule()` provides a cross-browser text-to-clipboard utility and UI state manager (`createClipboardButtonState`).
- `contribution-calendar.js`: `dashboardContributionCalendarModule()` renders the GitHub-style contribution grid for usage activity.
- `conversation-drawer.js`: `dashboardConversationDrawerModule()` fetches and reconstructs chat threads from audit logs to render the sliding conversation UI.
- `conversation-helpers.js`: Parses complex JSON arrays/objects into clean text payloads for the UI.
- `date-picker.js`: `dashboardDatePickerModule()` handles the custom date range dropdown logic.
- `execution-plans.js`: `dashboardExecutionPlansModule()` handles the workflow builder UI, node connections, execution previews, and saving plans.
- `guardrails.js`: `dashboardGuardrailsModule()` manages the dynamic forms for authoring system prompt and LLM-altering policies.
- `timezone.js`: `dashboardTimezoneModule()` detects the browser's timezone, manages local storage overrides, and formats timestamps across the UI.
- `usage.js`: `dashboardUsageModule()` handles fetching and rendering the semantic cache analytics, daily tokens, and model cost statistics.

### `./internal/admin/dashboard/templates/`

Contains the Go HTML templates for the dashboard structure.

- `audit-pane.html`: Template for the Request/Response JSON viewers.
- `auth-banner.html`: Banner showing missing API key warnings.
- `date-picker.html`: The interactive calendar date picker dropdown.
- `edit-icon.html`, `x-icon.html`: SVG icon fragments.
- `execution-plan-chart.html`: The visual node-based workflow layout.
- `helper-disclosure.html`: The inline tooltip/help text component.
- `index.html`: The main content panes for all dashboard routes.
- `layout.html`: The root HTML shell, sidebar navigation, and script tags.
- `pagination.html`: Reusable pagination controls.

---

## `./internal/aliases/`

It's responsible for managing model aliases (e.g., routing `smart-model` to `openai/gpt-4o`).

### `./internal/aliases/batch_preparer.go`

It intercepts batch processing to rewrite custom alias names to their target model names inside batch JSONL files.

- `BatchPreparer`: Wraps a provider and alias service.
- `NewBatchPreparer()`: Constructor.
- `PrepareBatchRequest()`: Rewrites the top-level batch request and JSONL file contents.
- `batchFileTransport()`: Extracts the native file provider.
- `aliasModelSupportChecker`, `aliasModelProviderTypeChecker`: Interfaces for validating models.
- `resolveAliasModel()`, `resolveAliasRequestSelector()`, `resolveAliasRoutableSelector()`, `validateResolvedProviderType()`: Resolution logic.
- `rewriteAliasChatRequest()`, `rewriteAliasResponsesRequest()`, `rewriteAliasEmbeddingRequest()`: Replaces models inside individual payload types.
- `rewriteAliasBatchSource()`: Traverses the batch JSONL and mutates known endpoints.

### `./internal/aliases/factory.go`

It sets up the aliases module dependencies.

- `Result`: Holds the initialized Service and Store.
- `Close()`: Shuts down the background refresher and DB connections.
- `New()`, `NewWithSharedStorage()`, `newResult()`, `createStore()`: Bootstraps the service and DB implementations.

### `./internal/aliases/provider.go`

It wraps `core.RoutableProvider` to intercept streaming and synchronous requests to resolve aliases before sending them upstream.

- `Provider`: Middleware provider implementation.
- `requestRewriteMode`, `Options`: Configuration for the provider behavior.
- `NewProvider()`, `NewProviderWithOptions()`: Constructors.
- `ResolveModel()`: Translates the model selector.
- `ChatCompletion()`, `StreamChatCompletion()`, `Responses()`, `StreamResponses()`, `Embeddings()`: Request interceptors.
- `ListModels()`, `Supports()`, `GetProviderType()`, `GetProviderName()`, `ModelCount()`, `NativeFileProviderTypes()`: Registry bypass methods.
- `CreateBatch()`, `GetBatch()`, `ListBatches()`, `CancelBatch()`, `GetBatchResults()`, `CreateBatchWithHints()`, `GetBatchResultsWithHints()`, `ClearBatchResultHints()`: Batch delegation methods.
- `CreateFile()`, `ListFiles()`, `GetFile()`, `DeleteFile()`, `GetFileContent()`: File delegation methods.
- `Passthrough()`, `PrepareBatchRequest()`: Passthrough delegation.
- `recordBatchPreparation()`, `cleanupSupersededBatchRewriteFile()`, `cleanupBatchRewriteFile()`, `mergeBatchHints()`, `providerValueForMode()`, `nativeBatchRouter()`, `nativeBatchHintRouter()`, `nativeFileRouter()`, `batchFileTransport()`, `passthroughRouter()`: Helper utilities for batch execution.

### `./internal/aliases/service.go`

It holds the central business logic and active memory snapshot of aliases.

- `Catalog`: Interface abstracting the underlying provider registry.
- `snapshot`: The in-memory struct holding mapped aliases.
- `Service`: Core logic controller.
- `NewService()`: Constructor.
- `Refresh()`: Reads all aliases from the DB and updates the snapshot.
- `List()`, `ListViews()`, `Get()`: Retrieves aliases.
- `Resolve()`, `resolveRequested()`, `ResolveModel()`: Translates an alias to its target.
- `Supports()`, `GetProviderType()`, `ExposedModels()`, `ExposedModelsFiltered()`, `exposedModelsFiltered()`: Integrates aliases into the public model inventory.
- `Upsert()`, `Delete()`, `validate()`: Modifies aliases in the database.
- `resolveAlias()`, `StartBackgroundRefresh()`: Resolution internals and worker loop.

### `./internal/aliases/store.go`

It defines the database interfaces and normalization logic.

- `ValidationError`: Struct for data constraint errors.
- `IsValidationError()`, `newValidationError()`: Error helpers.
- `Store`: The CRUD interface for aliases.
- `aliasScanner`, `aliasRows`: Database agnostic interfaces.
- `normalizeName()`, `normalizeAlias()`, `collectAliases()`: Formats inputs before DB insertion.

### `./internal/aliases/store_mongodb.go`

- `mongoAliasDocument`, `mongoAliasIDFilter`: BSON definitions.
- `MongoDBStore`: MongoDB implementation of `Store`.
- `NewMongoDBStore()`, `List()`, `Get()`, `Upsert()`, `Delete()`, `Close()`, `aliasFromMongo()`: CRUD methods.

### `./internal/aliases/store_postgresql.go`

- `PostgreSQLStore`: PostgreSQL implementation of `Store`.
- `NewPostgreSQLStore()`, `List()`, `Get()`, `Upsert()`, `Delete()`, `Close()`, `scanPostgreSQLAlias()`: CRUD methods.

### `./internal/aliases/store_sqlite.go`

- `SQLiteStore`: SQLite implementation of `Store`.
- `NewSQLiteStore()`, `List()`, `Get()`, `Upsert()`, `Delete()`, `Close()`, `scanSQLiteAlias()`, `boolToSQLite()`: CRUD methods.

### `./internal/aliases/types.go`

- `Alias`: The domain object for a model alias.
- `TargetSelector()`: Returns a parsed target selector.
- `Resolution`: Struct containing the requested name, the resolved concrete name, and the alias applied.
- `View`: Struct extending an Alias with runtime availability data for the UI.

---

## `./internal/app/`

It manages the high-level application container wiring and shutdown sequences.

### `./internal/app/app.go`

- `App`: Main application struct containing pointers to providers, DBs, loggers, caches, and the HTTP server.
- `Config`: App initialization arguments.
- `New()`: Bootstraps and injects dependencies across storage, cache, usage, audit, guardrails, execution plans, and routing.
- `Router()`, `AuditLogger()`, `UsageLogger()`, `providerAsNativeFileRouter()`: Exposes components for testing and internal routing.
- `Start()`: Begins listening for HTTP traffic.
- `Shutdown()`: Coordinates stopping background refresh loops, closing HTTP connections, flushing caches, and closing database connections.
- `logStartupInfo()`, `initAdmin()`, `configGuardrailDefinitions()`, `defaultExecutionPlanInput()`, `dashboardRuntimeConfig()`, `cacheAnalyticsConfigured()`, `dashboardEnabledValue()`, `dashboardFallbackModeValue()`, `runtimeExecutionFeatureCaps()`, `executionPlanRefreshInterval()`, `responseCacheConfigured()`, `simpleResponseCacheConfigured()`, `simpleResponseCacheConfiguredFromResponse()`, `semanticResponseCacheConfigured()`, `semanticResponseCacheConfiguredFromResponse()`, `fallbackFeatureEnabledGlobally()`, `fallbackModeEnabled()`, `firstSharedStorage()`: Startup configuration and logging helpers.

---

## `./internal/auditlog/`

It's responsible for capturing, formatting, and storing records of all LLM traffic.

### `./internal/auditlog/auditlog.go`

- `LogStore`: Interface for database bulk writers.
- `LogEntry`: The top-level struct representing an HTTP request execution.
- `LogData`: Detailed JSON payload representing headers, requests, responses, errors, and metadata.
- `ExecutionFeaturesSnapshot`: Embedded representation of the workflow capabilities triggered by the request.
- `marshalLogData()`, `normalizeCacheType()`, `displayAuditProviderName()`: Formatters.
- `RedactedHeaders`, `redactedHeadersSet`, `RedactHeaders()`: Cleanses sensitive HTTP headers (e.g., API keys, cookies) before logging.
- `Config`, `DefaultConfig()`: Audit logger settings.

### `./internal/auditlog/cleanup.go`

- `CleanupInterval`: Constant representing the interval (1 Hour).
- `RunCleanupLoop()`: Triggers the DB deletion routine to prune logs past their retention policy.

### `./internal/auditlog/constants.go`

- `MaxBodyCapture`, `MaxContentCapture`, `BatchFlushThreshold`, `APIKeyHashPrefixLength`: Tuning constants.
- `LogEntryKey`, `LogEntryStreamingKey`: Request context keys for accessing the current entry in middleware.

### `./internal/auditlog/conversation_helpers.go`

- `entryLookup`: Function type for querying DB records.
- `buildConversationThread()`: Reconstructs a full chat history backwards and forwards by following `previous_response_id` links.
- `extractResponseID()`, `extractPreviousResponseID()`, `extractStringField()`, `extractTrimmedString()`, `clampConversationLimit()`: Extraction helpers mapping over generic JSON shapes.

### `./internal/auditlog/entry_capture.go`

- `PopulateRequestData()`: Reads HTTP headers and the captured snapshot body into the `LogEntry`.
- `PopulateResponseHeaders()`, `PopulateResponseData()`: Extracts HTTP response properties.
- `CaptureInternalJSONExchange()`: Bypasses the HTTP stack to directly inject internal service calls (e.g., synthetic guardrail requests) into the audit log.
- `ensureLogData()`, `requestIDForEntry()`, `internalJSONAuditRequest()`, `internalJSONAuditRequestBody()`, `internalJSONAuditResponse()`, `internalJSONAuditHeaders()`, `boundedAuditBody()`: Extraction internals preventing out-of-memory errors on massive payloads.

### `./internal/auditlog/factory.go`

- `Result`: Container for the initialized Logger and Storage.
- `Close()`: Lifecycle method.
- `New()`, `createLogStore()`, `buildLoggerConfig()`: Dependency injection initializers.

### `./internal/auditlog/logger.go`

- `Logger`: The background worker that buffers logs and flushes them efficiently.
- `NewLogger()`: Constructor.
- `Write()`: Enqueues an entry (or drops it if the buffer is overflowing to prevent gateway lag).
- `Config()`, `Close()`: Lifecycle methods.
- `flushLoop()`, `flushBatch()`: Goroutine executing the DB writes.
- `NoopLogger`, `LoggerInterface`: Abstraction for environments where audit logging is disabled.

### `./internal/auditlog/middleware.go`

- `Middleware()`: The primary Echo HTTP interceptor that initializes the log timer and injects the `responseBodyCapture`.
- `applyExecutionPlan()`, `applyAuthentication()`, `enrichEntryWithExecutionPlan()`, `resolvedModelForAuditLog()`, `captureLoggedRequestBody()`, `captureLoggedResponseBody()`, `captureLoggedBody()`: Enhancers for the log entry before persistence.
- `responseBodyCapture`: Custom HTTP ResponseWriter that duplicates written bytes into a memory buffer (up to `MaxBodyCapture`).
- `captureEnabled()`, `Flush()`, `Hijack()`, `Unwrap()`, `shouldCaptureResponseBody()`, `isEventStreamContentType()`: ResponseWriter method overrides.
- `extractHeaders()`, `hashAPIKey()`: Security formatting.
- `EnrichEntry()`, `EnrichEntryWithExecutionPlan()`, `EnrichLogEntryWithExecutionPlan()`, `EnrichEntryWithResolvedRoute()`, `EnrichLogEntryWithResolvedRoute()`, `enrichEntryWithResolvedRoute()`, `EnrichEntryWithCacheType()`, `EnrichEntryWithAuthMethod()`, `EnrichEntryWithAuthKeyID()`, `EnrichEntryWithUserPath()`, `EnrichLogEntryWithRequestContext()`, `auditEnabledForContext()`, `EnrichEntryWithError()`, `EnrichEntryWithStream()`, `toValidUTF8String()`, `decompressBody()`: A suite of context injection and enrichment tools used by downstream services to tag the current log entry.

### `./internal/auditlog/reader.go`

- `QueryParams`, `LogQueryParams`: Structures defining the search criteria and pagination logic.
- `LogListResult`, `ConversationResult`: Structures defining the output shape for the UI.
- `Reader`: Interface for executing queries.

### `./internal/auditlog/reader_factory.go`

- `NewReader()`: Dispatches creation to the active database implementation.

### `./internal/auditlog/reader_helpers.go`

- `buildWhereClause()`, `escapeLikeWildcards()`, `clampLimitOffset()`: Query construction helpers.

### `./internal/auditlog/reader_mongodb.go`

- `MongoDBReader`, `mongoLogRow`: Implementations of `Reader`.
- `toLogEntry()`, `sanitizeLogData()`: BSON to Domain converters.
- `NewMongoDBReader()`, `mongoUserPathMatchFilter()`, `GetLogs()`, `firstNonEmpty()`, `GetLogByID()`, `GetConversation()`, `mongoDateRangeFilter()`, `findByResponseID()`, `findByPreviousResponseID()`, `findFirstByField()`: Query methods.

### `./internal/auditlog/reader_postgresql.go`

- `PostgreSQLReader`: Implementation of `Reader`.
- `NewPostgreSQLReader()`, `GetLogs()`, `GetLogByID()`, `GetConversation()`, `pgDateRangeConditions()`, `findByResponseID()`, `findByPreviousResponseID()`, `scanPostgreSQLLogEntry()`: Query methods.

### `./internal/auditlog/reader_sqlite.go`

- `SQLiteReader`: Implementation of `Reader`.
- `NewSQLiteReader()`, `GetLogs()`, `GetLogByID()`, `GetConversation()`, `sqliteDateRangeConditions()`, `sqliteTimestampBoundary()`, `parseSQLTimestamp()`, `findByResponseID()`, `findByPreviousResponseID()`, `scanSQLiteLogEntry()`: Query methods.

### `./internal/auditlog/store_mongodb.go`

- `ErrPartialWrite`, `PartialWriteError`, `auditLogPartialWriteFailures`: Metric and error handling for bulk writes.
- `MongoDBStore`: Implementation of `LogStore`.
- `NewMongoDBStore()`, `WriteBatch()`, `Flush()`, `Close()`: Write operations.

### `./internal/auditlog/store_postgresql.go`

- `auditLogBatchExecutor`: Interface abstracting transactions.
- `PostgreSQLStore`: Implementation of `LogStore`.
- `NewPostgreSQLStore()`, `WriteBatch()`, `writeBatchSmall()`, `writeBatchLarge()`, `writeAuditLogInsertChunks()`, `buildAuditLogInsert()`, `renamePostgreSQLAuditColumn()`, `postgresqlColumnExists()`, `Flush()`, `Close()`, `cleanup()`: Write and lifecycle operations.

### `./internal/auditlog/store_sqlite.go`

- `SQLiteStore`: Implementation of `LogStore`.
- `NewSQLiteStore()`, `WriteBatch()`, `Flush()`, `Close()`, `cleanup()`, `renameSQLiteAuditColumn()`, `sqliteColumnExists()`: Write and lifecycle operations.

### `./internal/auditlog/stream_observer.go`

- `StreamLogObserver`: Hooks into the `streaming.ObservedSSEStream` to accumulate fragments of SSE data.
- `NewStreamLogObserver()`, `OnJSONEvent()`, `OnStreamClose()`, `parseChatCompletionEvent()`, `parseResponsesAPIEvent()`, `appendContent()`: Event triggers that aggregate chunks and save them to the final DB entry.

### `./internal/auditlog/stream_wrapper.go`

- `streamResponseBuilder`: Maintains state while reconstructing a full JSON response from SSE chunks.
- `buildChatCompletionResponse()`, `buildResponsesAPIResponse()`: Reconstructs standard formats.
- `CreateStreamEntry()`, `copyMap()`, `GetStreamEntryFromContext()`, `MarkEntryAsStreaming()`, `IsEntryMarkedAsStreaming()`: Utility functions for forking log entries to handle async stream closing.

### `./internal/auditlog/user_path_filter.go`

- `normalizeAuditUserPathFilter()`, `auditUserPathSubtreePattern()`, `auditUserPathSQLPredicate()`, `auditUserPathSubtreeRegex()`: Translates `/team/alpha` into DB-specific LIKE/Regex filters mapping descendants.

---

## `./internal/authkeys/`

It's responsible for managing and validating API keys.

### `./internal/authkeys/factory.go`

- `Result`: Lifecycle wrapper.
- `Close()`, `New()`, `NewWithSharedStorage()`, `newResult()`, `createStore()`: Dependency injection.

### `./internal/authkeys/service.go`

- `snapshot`, `AuthenticationResult`: In-memory state and validation returns.
- `Service`: Core logic manager.
- `NewService()`, `Refresh()`, `Enabled()`, `Total()`, `ActiveCount()`, `ListViews()`: State queries.
- `Create()`: Issues a new API key with randomized entropy.
- `Deactivate()`: Permanently disables a key.
- `Authenticate()`: Extremely fast validation checking incoming bearer tokens against the cached snapshot via SHA256.
- `StartBackgroundRefresh()`, `authenticateKey()`, `refreshBestEffort()`, `applyUpsert()`, `applyDeactivate()`, `cloneSnapshot()`, `sortSnapshotOrder()`: Cache logic.
- `generateTokenMaterial()`, `parseTokenSecret()`, `hashSecret()`, `redactTokenValue()`: Cryptography helpers (generates `sk_gom_...` tokens).

### `./internal/authkeys/store.go`

- `ValidationError`, `IsValidationError()`, `newValidationError()`, `ErrNotFound`, `ErrInvalidToken`, `ErrInactive`, `ErrExpired`: Core errors.
- `Store`, `authKeyScanner`, `authKeyRows`: Interfaces for DB operations.
- `normalizeCreateInput()`, `normalizeID()`, `collectAuthKeys()`: Validation and parsing.

### `./internal/authkeys/store_mongodb.go`

- `mongoAuthKeyDocument`, `mongoAuthKeyIDFilter`: BSON definitions.
- `MongoDBStore`: Implementation of `Store`.
- `NewMongoDBStore()`, `List()`, `Create()`, `Deactivate()`, `Close()`, `authKeyFromMongo()`, `timePtrUTC()`: DB operations.

### `./internal/authkeys/store_postgresql.go`

- `PostgreSQLStore`: Implementation of `Store`.
- `NewPostgreSQLStore()`, `List()`, `Create()`, `Deactivate()`, `Close()`, `scanPostgreSQLAuthKey()`, `pgUnixOrNil()`, `int64PtrToTime()`, `pgNullableString()`, `derefTrimmedString()`: DB operations.

### `./internal/authkeys/store_sqlite.go`

- `SQLiteStore`: Implementation of `Store`.
- `NewSQLiteStore()`, `List()`, `Create()`, `Deactivate()`, `Close()`, `scanSQLiteAuthKey()`, `isSQLiteDuplicateColumnError()`, `boolToSQLite()`, `unixOrNil()`, `unixPtr()`, `nullableString()`, `nullableStringValue()`: DB operations.

### `./internal/authkeys/types.go`

- `AuthKey`, `View`, `IssuedKey`, `CreateInput`: Domain models mapping DB rows to JSON and internal logic.
- `Active()`: Domain logic determining if an `AuthKey` is currently valid.

---

## `./internal/batch/`

It tracks asynchronous offline jobs (OpenAI Native Batches) generated by gateway clients.

### `./internal/batch/factory.go`

- `Result`, `Close()`, `New()`, `NewWithSharedStorage()`, `createStore()`: Standard DI lifecycle.

### `./internal/batch/store.go`

- `StoredBatch`: Domain entity wrapping `core.BatchResponse` with routing metadata.
- `Store`: Interface for persistence.
- `normalizeLimit()`, `cloneBatch()`, `serializeBatch()`, `deserializeBatch()`, `normalizeStoredBatch()`, `splitGatewayBatchMetadata()`, `parseUsageLoggedAt()`, `EffectiveUsageEnabled()`: SerDe utilities.

### `./internal/batch/store_memory.go`

- `MemoryStore`: In-memory implementation of `Store` (used when running without a persistent DB).
- `NewMemoryStore()`, `Create()`, `Get()`, `List()`, `Update()`, `Close()`: Map-backed operations.

### `./internal/batch/store_mongodb.go`

- `mongoBatchDocument`, `MongoDBStore`: BSON definitions and DB implementation.
- `NewMongoDBStore()`, `Create()`, `Get()`, `List()`, `Update()`, `Close()`: DB operations.

### `./internal/batch/store_postgresql.go`

- `PostgreSQLStore`: DB implementation.
- `NewPostgreSQLStore()`, `Create()`, `Get()`, `List()`, `Update()`, `Close()`: DB operations.

### `./internal/batch/store_sqlite.go`

- `SQLiteStore`: DB implementation.
- `NewSQLiteStore()`, `Create()`, `Get()`, `List()`, `Update()`, `Close()`: DB operations.

---

## `./internal/cache/`

It provides general-purpose caching interfaces.

### `./internal/cache/redis.go`

- `RedisStoreConfig`, `RedisStore`: Implements basic caching via Redis.
- `NewRedisStore()`, `Get()`, `Set()`, `Close()`: Wraps go-redis.

### `./internal/cache/store.go`

- `Store`: Generic interface.
- `MapStore`: Thread-safe map implementation.
- `NewMapStore()`, `Get()`, `Set()`, `Close()`: In-memory generic operations.

### `./internal/cache/modelcache/local.go`

- `LocalCache`: Caches the massive AI Model Registry JSON to a local disk file to survive restarts.
- `NewLocalCache()`, `Get()`, `Set()`, `Close()`: File I/O operations (atomic rename).

### `./internal/cache/modelcache/modelcache.go`

- `ModelCache`, `CachedProvider`, `CachedModel`: Internal structs representing the cached representation of the model registry.
- `Cache`: Interface for the specialized model caching.

### `./internal/cache/modelcache/redis.go`

- `RedisModelCacheConfig`, `redisModelCache`: Caches the model registry JSON to Redis.
- `NewRedisModelCache()`, `NewRedisModelCacheWithStore()`, `Get()`, `Set()`, `Close()`: Implementation bridging `ModelCache` domain models into the Redis store.

---

## `./internal/core/`

It holds the foundational domain representations, logic interfaces, and data structures.

### `./internal/core/batch.go`

- `BatchRequest`, `BatchRouteInfo`, `BatchRequestItem`, `BatchResponse`, `BatchListResponse`, `BatchResultsResponse`, `BatchRequestCounts`, `BatchResultItem`, `BatchError`, `BatchUsageSummary`: Deep mappings of the OpenAI Batch API schemas.

### `./internal/core/batch_json.go`

- Custom `MarshalJSON` / `UnmarshalJSON` for Batch structs that dynamically captures `UnknownJSONFields` to ensure undocumented provider properties are never stripped from the payload when routing.

### `./internal/core/batch_preparation.go`

- `BatchPreparationMetadata`: Tracks context about dynamically rewritten file IDs.
- `BatchFileTransport`, `BatchItemRewriteFunc`, `BatchRewriteResult`: Interfaces for the batch mutation process.
- `RewriteBatchSource()`, `rewriteInlineBatchItems()`, `rewriteInputFileBatch()`, `rewriteBatchJSONLContent()`, `wrapBatchInputFileLineError()`, `recordBatchEndpointHint()`, `mergeBatchEndpointHints()`, `cloneBatchRequest()`, `cloneBatchRequestItem()`, `cloneBatchStringMap()`, `jsonSemanticallyEqual()`, `reflectDeepEqualJSON()`: High-level logic that downloads a JSONL file, parses it line by line, resolves aliases/guardrails on individual lines, re-uploads it as a new file to the provider, and updates the batch.

### `./internal/core/batch_semantic.go`

- `DecodedBatchItemRequest`, `DecodedBatchItemHandlers`: Provides a type-safe way to unpack the nested strings in a `.jsonl` payload into strong `ChatRequest`/`ResponsesRequest` instances.
- `ChatRequest()`, `ResponsesRequest()`, `EmbeddingRequest()`, `ModelSelector()`, `RequestedModelSelector()`, `DispatchDecodedBatchItem()`, `NormalizeOperationPath()`, `ResolveBatchItemEndpoint()`, `MaybeDecodeKnownBatchItemRequest()`, `DecodeKnownBatchItemRequest()`, `BatchItemModelSelector()`, `BatchItemRequestedModelSelector()`: Converters interpreting endpoint URLs into schemas.

### `./internal/core/chat_content.go`

- `ContentPart`, `ImageURLContent`, `InputAudioContent`: Represents standard multimodal message parts.
- `UnmarshalJSON`, `MarshalJSON`: Custom JSON handlers supporting OpenAI schemas.
- `UnmarshalMessageContent()`, `NormalizeMessageContent()`, `ExtractTextContent()`, `HasStructuredContent()`, `HasNonTextContent()`, `NormalizeContentParts()`, `joinTextParts()`, `partsText()`, `interfacePartsText()`, `unmarshalContentPart()`, `normalizeTypedContentPart()`, `normalizeContentPartValue()`, `normalizeContentPartMap()`, `unmarshalImageURLContent()`, `unmarshalInputAudioContent()`: Extensive parsing utilities to handle cases where `content` can be a string, an array of objects, or `null`.

### `./internal/core/chat_json.go`

- Custom JSON serialization preserving unknown properties for `ChatRequest`.

### `./internal/core/context.go`

- Core context keys (`requestIDKey`, `executionPlanKey`, etc.).
- `RequestOrigin`: Enum determining if a request is from a client or an internal background system (e.g. `guardrail`).
- `WithRequestID()`, `GetRequestID()`, `WithRequestSnapshot()`, `GetRequestSnapshot()`, `WithWhiteBoxPrompt()`, `GetWhiteBoxPrompt()`, `WithExecutionPlan()`, `GetExecutionPlan()`, `WithAuthKeyID()`, `GetAuthKeyID()`, `WithEffectiveUserPath()`, `GetEffectiveUserPath()`, `WithBatchPreparationMetadata()`, `GetBatchPreparationMetadata()`, `WithEnforceReturningUsageData()`, `GetEnforceReturningUsageData()`, `WithGuardrailsHash()`, `GetGuardrailsHash()`, `WithFallbackUsed()`, `GetFallbackUsed()`, `WithRequestOrigin()`, `GetRequestOrigin()`: Getters and setters for dependency injection throughout the middleware stack.

### `./internal/core/embeddings_json.go`

- Custom JSON serialization for `EmbeddingRequest`.

### `./internal/core/endpoints.go`

- `BodyMode`, `Operation`, `EndpointDescriptor`: Enums defining how an HTTP path should be treated (e.g., is it JSON? is it a model invocation?).
- `DescribeEndpoint()`, `DescribeEndpointPath()`, `describeEndpointPath()`, `bodyModeForEndpoint()`, `matchesEndpointPath()`, `normalizeEndpointPath()`, `IsModelInteractionPath()`, `ParseProviderPassthroughPath()`: Routing rules and heuristics.

### `./internal/core/errors.go`

- `ErrorType`, `GatewayError`: Unified HTTP error representation.
- `Error()`, `Unwrap()`, `HTTPStatusCode()`, `ToJSON()`, `WithParam()`, `WithCode()`: Error properties.
- `NewProviderError()`, `NewRateLimitError()`, `NewInvalidRequestError()`, `NewInvalidRequestErrorWithStatus()`, `NewAuthenticationError()`, `NewNotFoundError()`, `ParseProviderError()`: Factory functions standardizing varied downstream AI errors into predictable HTTP status codes.

### `./internal/core/execution_plan.go`

- `ExecutionMode`, `CapabilitySet`, `ExecutionFeatures`: Represents flags defining what features (Caching, Audit, Guardrails) apply to an execution.
- `ExecutionPlanSelector`: Identifies a request's scope (User Path, Provider, Model).
- `ResolvedExecutionPolicy`, `ExecutionPlan`: The finalized payload attached to a Context determining the request's destiny.
- `ApplyUpperBound()`, `DefaultExecutionFeatures()`, `NewExecutionPlanSelector()`, `RequestedQualifiedModel()`, `ResolvedQualifiedModel()`, `ExecutionPlanVersionID()`, `CacheEnabled()`, `AuditEnabled()`, `UsageEnabled()`, `GuardrailsEnabled()`, `FallbackEnabled()`, `GuardrailsHash()`, `featureEnabled()`: Methods computing flags.

### `./internal/core/files.go`

- `FileCreateRequest`, `FileRouteInfo`, `FileObject`, `FileListResponse`, `FileDeleteResponse`, `FileContentResponse`: Structs bridging Native File APIs.
- `FileMultipartMetadataReader`, `EnrichFileCreateRouteInfo()`, `ensureParsedLimit()`: Parsing multipart uploads.

### `./internal/core/interfaces.go`

- The defining interfaces that all providers must implement: `Provider`, `NativeBatchProvider`, `BatchCreateHintAwareProvider`, `BatchResultHintAwareProvider`, `NativeBatchRoutableProvider`, `NativeBatchHintRoutableProvider`, `NativeFileProvider`, `NativeFileRoutableProvider`, `NativeFileProviderTypeLister`, `RoutableProvider`, `ProviderNameResolver`, `ProviderTypeNameResolver`, `ProviderNameTypeResolver`, `AvailabilityChecker`, `ModelLookup`.

### `./internal/core/json_fields.go`

- `UnknownJSONFields`: A raw byte buffer encapsulating unrecognized fields in a JSON object.
- `CloneRawJSON()`, `CloneUnknownJSONFields()`, `UnknownJSONFieldsFromMap()`, `unknownJSONFieldsFromMap()`, `Lookup()`, `IsEmpty()`, `extractUnknownJSONFields()`, `containsJSONField()`, `marshalWithUnknownJSONFields()`, `mergeUnknownJSONObject()`, `mergedJSONObjectCap()`: Highly optimized byte manipulation to safely inject and extract known properties without losing vendor-specific undocumented fields.

### `./internal/core/message_json.go`

- Custom `UnmarshalJSON` and `MarshalJSON` implementing `UnknownJSONFields` for `Message`, `ResponseMessage`, `ToolCall`, and `FunctionCall`.
- `marshalMessageContent()`, `isNullEquivalentContent()`: Handles rendering missing/empty message contents without causing provider errors.

### `./internal/core/model_selector.go`

- `ModelSelector`: Struct representing `{provider}/{model}` parsed from a string.
- `QualifiedModel()`, `ParseModelSelector()`, `splitQualifiedModel()`: String extraction.

### `./internal/core/passthrough.go`

- `PassthroughRequest`, `PassthroughResponse`, `PassthroughProvider`, `RoutablePassthrough`, `PassthroughSemanticEnricher`: Structs defining raw reverse-proxy logic that bypasses strict schema translation.

### `./internal/core/request_model_resolution.go`

- `RequestModelResolution`: Tracks the state transformation of a model target. Did it trigger an alias? What's the final explicit provider?
- `RequestedQualifiedModel()`, `ResolvedQualifiedModel()`.

### `./internal/core/request_snapshot.go`

- `RequestSnapshot`: Complete immutable capture of an HTTP request.
- `NewRequestSnapshot()`, `NewRequestSnapshotWithOwnedBody()`, `newRequestSnapshot()`, `firstUserPath()`, `WithUserPath()`, `CapturedBody()`, `CapturedBodyView()`, `GetRouteParams()`, `GetQueryParams()`, `GetHeaders()`, `GetTraceMetadata()`, `cloneBytes()`, `cloneStringMap()`, `cloneMultiMap()`: Immutability helpers.

### `./internal/core/requested_model_selector.go`

- `RequestedModelSelector`: Initial unparsed state representation.
- `NewRequestedModelSelector()`, `Normalize()`, `RequestedQualifiedModel()`.

### `./internal/core/responses.go`

- `ResponsesRequest`, `ResponsesInputElement`, `ResponsesResponse`, `ResponsesOutputItem`, `ResponsesContentItem`, `ResponsesUsage`, `ResponsesError`: Schemas mapping to the emerging unified internal representation (modeled after newer OAI features).
- `semanticSelector()`, `WithStreaming()`.

### `./internal/core/responses_json.go`

- Custom unmarshalers/marshalers applying `UnknownJSONFields` for the `Responses` schemas. `stringifyRawValue()`.

### `./internal/core/semantic.go`

- `RouteHints`, `PassthroughRouteInfo`, `semanticCacheKey`, `WhiteBoxPrompt`: Central data stores for semantic caching computation.
- `CachedChatRequest()`, `CachedResponsesRequest()`, `CachedEmbeddingRequest()`, `CachedBatchRequest()`, `CachedBatchRouteInfo()`, `CachedFileRouteInfo()`, `CachedPassthroughRouteInfo()`, `CanonicalSelectorFromCachedRequest()`, `cacheValue()`, `cachedSemanticValue()`, `cachedSemanticAny()`, `cacheBatchRouteMetadata()`, `CacheFileRouteInfo()`, `CachePassthroughRouteInfo()`, `DeriveWhiteBoxPrompt()`, `derivePassthroughRouteInfoFromTransport()`, `deriveSnapshotSelectorHintsGJSON()`, `snapshotSelectorStringAllowed()`, `snapshotSelectorBoolAllowed()`, `DeriveFileRouteInfoFromTransport()`, `DeriveBatchRouteInfoFromTransport()`, `fileActionFromTransport()`, `fileIDFromTransport()`, `batchActionFromTransport()`, `batchIDFromTransport()`, `firstTransportValue()`: Huge mapping layer analyzing arbitrary request payloads to determine properties that apply to caching rules.

### `./internal/core/semantic_canonical.go`

- `canonicalJSONSpec`, `semanticSelectorCarrier`, `canonicalOperationCodec`: Generic decoders mapping raw requests into types implementing `Operation`.
- `unmarshalCanonicalJSON()`, `newCanonicalOperationCodec()`, `canonicalOperationCodecFor()`, `decodeCanonicalOperation()`, `DecodeChatRequest()`, `DecodeResponsesRequest()`, `DecodeEmbeddingRequest()`, `DecodeBatchRequest()`, `parseRouteLimit()`, `cachedRouteMetadata()`, `BatchRouteMetadata()`, `FileRouteMetadata()`, `NormalizeModelSelector()`, `DecodeCanonicalSelector()`, `decodeCanonicalJSON()`, `cacheSemanticSelectorHints()`, `cacheSemanticStreamHint()`, `cacheSemanticSelectorHintsFromRequest()`, `semanticSelectorFromCanonicalRequest()`: Centralized parsing routing based on endpoint type.

### `./internal/core/types.go`

- `StreamOptions`, `Reasoning`, `ChatRequest`, `MessageContent`, `Message`, `ToolCall`, `FunctionCall`, `ChatResponse`, `Choice`, `ResponseMessage`, `PromptTokensDetails`, `CompletionTokensDetails`, `Usage`, `Model`, `ModelMetadata`, `ModelRanking`, `ModelCategory` (`CategoryAll`, `CategoryTextGeneration`, etc.), `ModelPricing`, `ModelPricingTier`, `ModelsResponse`, `EmbeddingRequest`, `EmbeddingResponse`, `EmbeddingData`, `EmbeddingUsage`: Fundamental struct definitions mirroring the public API boundary.
- `semanticSelector()`, `WithStreaming()`, `CategoriesForModes()`, `AllCategories()`: Helpers.

### `./internal/core/user_path.go`

- `NormalizeUserPath()`, `UserPathAncestors()`, `UserPathFromContext()`: Handles hierarchical API key restrictions (`/team/a/b` implies `/team`).

---

## `./internal/embedding/`

It generates vector embeddings.

### `./internal/embedding/embedding.go`

- `Embedder`: Interface.
- `NewEmbedder()`: Configures a client against a generic HTTP LLM Provider configured in the YAML.
- `apiEmbedder`: Implementation.
- `embeddingRequest`, `embeddingResponse`: API schemas.
- `normalizeGeminiEmbeddingModel()`, `openAIEmbeddingsEndpointURL()`, `Embed()`, `Identity()`, `Close()`: Execution flow.

---

## `./internal/executionplans/`

It evaluates conditional workflows (Guardrails, Cache logic, Fallbacks) based on endpoints.

### `./internal/executionplans/compiler.go`

- `compiler`: Connects database-stored workflow instructions to executable code modules.
- `NewCompiler()`, `NewCompilerWithFeatureCaps()`: Initializers.
- `Compile()`: Converts a `Version` row into a `CompiledPlan` featuring an actionable `guardrails.Pipeline`.
- `compileGuardrails()`: Builds the executable guardrail list.

### `./internal/executionplans/factory.go`

- `Result`, `Close()`, `New()`, `NewWithSharedStorage()`, `newResult()`, `createStore()`: Instantiates the service and DB interface.

### `./internal/executionplans/service.go`

- `CompiledPlan`: Holds the `Version`, the context `ResolvedExecutionPolicy`, and the `Pipeline`.
- `Compiler`, `snapshot`, `Service`: Manages in-memory lookup tries matching Request dimensions (Provider, Model, User Path) to Plans.
- `NewService()`, `Refresh()`, `refreshLocked()`, `EnsureDefaultGlobal()`, `Create()`, `Deactivate()`, `GetView()`, `ListViews()`, `Match()`, `PipelineForContext()`, `PipelineForExecutionPlan()`, `StartBackgroundRefresh()`, `matchCompiled()`, `validateCreateCandidate()`, `viewForVersion()`, `viewWithError()`, `rawScopeType()`, `rawScopeDisplay()`, `scopeType()`, `scopeDisplay()`, `viewScopeSpecificity()`, `snapshot()`, `cloneSnapshot()`, `compiledPlanForVersion()`, `storeActivatedCompiledLocked()`, `storeDeactivatedVersionLocked()`: Evaluates requests and returns the deepest matching plan.

### `./internal/executionplans/store.go`

- `ErrNotFound`, `ValidationError`: Standard errors.
- `IsValidationError()`, `newValidationError()`.
- `Store`: Interface specifying DB operations.

### `./internal/executionplans/store_helpers.go`

- `versionRowScanner`, `versionRowIterator`: Scan interfaces.
- `collectVersions()`, `storedScopeUserPath()`: Helpers decoding row data.

### `./internal/executionplans/store_mongodb.go`

- `mongoVersionDocument`, `MongoDBStore`: DB definitions.
- `NewMongoDBStore()`, `ListActive()`, `Get()`, `Create()`, `EnsureManagedDefaultGlobal()`, `Deactivate()`, `Close()`, `ensureManagedDefaultGlobal()`, `insertVersion()`, `versionFromMongo()`: CRUD.

### `./internal/executionplans/store_postgresql.go`

- `PostgreSQLStore`: DB definitions.
- `NewPostgreSQLStore()`, `ListActive()`, `Get()`, `Create()`, `EnsureManagedDefaultGlobal()`, `createVersion()`, `ensureManagedDefaultGlobal()`, `Deactivate()`, `Close()`, `scanPostgreSQLVersion()`, `nullIfEmpty()`, `valueOrEmpty()`, `isPostgreSQLUniqueViolation()`: CRUD.

### `./internal/executionplans/store_sqlite.go`

- `SQLiteStore`: DB definitions.
- `NewSQLiteStore()`, `isSQLiteDuplicateColumnError()`, `ListActive()`, `Get()`, `Create()`, `EnsureManagedDefaultGlobal()`, `Deactivate()`, `Close()`, `scanSQLiteVersion()`, `nullableString()`, `boolToSQLite()`: CRUD.

### `./internal/executionplans/types.go`

- `Scope`, `scopeJSON`, `Payload`, `FeatureFlags`, `GuardrailStep`, `Version`, `CreateInput`: Domain types specifying what triggers the workflow and what steps it takes.
- `MarshalJSON()`, `UnmarshalJSON()`, `canonicalize()`, `runtimeFeatures()`, `normalizeScope()`, `scopeKey()`, `normalizePayload()`, `normalizeCreateInput()`: Data sanitization.

### `./internal/executionplans/view.go`

- `View`: A flattened API representation for the UI containing errors and effective feature state.

---

## `./internal/fallback/`

It determines backup provider targets when models go offline.

### `./internal/fallback/resolver.go`

- `Registry`, `Resolver`: Evaluates dynamic metadata to return backup models.
- `NewResolver()`: Parses YAML/JSON logic.
- `ResolveFallbacks()`: Returns `[]ModelSelector`.
- `sourceModelInfo()`, `modeFor()`, `manualSelectorsFor()`, `autoSelectorsFor()`, `resolveSelector()`, `sourceKey()`, `matchKeys()`, `requiredCategoryForOperation()`, `supportsCategory()`, `preferredRanking()`, `rankingByName()`, `sameFamily()`, `capabilityOverlap()`, `absInt()`: Magic sorting algorithm that discovers "similar" models by extracting capabilities, Elo scores, and architecture family tags from the registry automatically.

---

## `./internal/guardrails/`

It provides modular middleware components altering request shapes natively.

### `./internal/guardrails/catalog.go`

- `Catalog`: Registry interface defining pipelines build logic.

### `./internal/guardrails/definitions.go`

- `Definition`, `View`, `TypeOption`, `TypeField`, `TypeDefinition`, `systemPromptDefinitionConfig`, `llmBasedAlteringDefinitionConfig`, `unavailableGuardrail`: Structs describing a rule for configuration files or databases.
- `ViewFromDefinition()`, `normalizeDefinition()`, `normalizeDefinitionType()`, `cloneDefinition()`, `cloneTypeDefinitions()`, `decodeSystemPromptDefinitionConfig()`, `decodeLLMBasedAlteringDefinitionConfig()`, `llmBasedAlteringRuntimeConfig()`, `buildDefinition()`, `summarizeDefinition()`, `TypeDefinitions()`, `llmBasedAlteringDescriptor()`, `mustMarshalRaw()`: Parsing and building logic to convert definitions into `Guardrail` logic implementations.

### `./internal/guardrails/executor.go`

- `RequestPatcher`, `BatchPreparer`: Wraps pipelines to apply them to native structures.
- `NewRequestPatcher()`, `PatchChatRequest()`, `PatchResponsesRequest()`, `NewBatchPreparer()`, `PrepareBatchRequest()`, `batchFileTransport()`, `processGuardedBatchRequest()`, `processGuardedChat()`, `processGuardedResponses()`: Invokes execution across messages correctly formatting their envelopes post-mutation.

### `./internal/guardrails/factory.go`

- `Result`, `Close()`, `New()`, `NewWithSharedStorage()`, `newResult()`, `createStore()`, `startGuardrailRefreshLoop()`, `validateExecutorCount()`: Lifecycle wiring.

### `./internal/guardrails/guardrails.go`

- `Message`: Agnostic representation of a message part.
- `Guardrail`: Interface requiring `Name` and `Process(ctx, []Message) ([]Message, error)`.

### `./internal/guardrails/llm_based_altering.go`

- `LLMBasedAlteringConfig`, `ChatCompletionExecutor`, `LLMBasedAlteringGuardrail`: Represents a powerful rule using a _secondary LLM_ to review and rewrite prompts strictly (e.g. to scrub PII tokens using `<TEXT_TO_ALTER>` wrappers).
- `DefaultLLMBasedAlteringPrompt`, `ResolveLLMBasedAlteringPrompt()`, `EffectiveLLMBasedAlteringMaxTokens()`, `NormalizeLLMBasedAlteringRoles()`, `NormalizeLLMBasedAlteringConfig()`, `NewLLMBasedAlteringGuardrail()`, `Name()`, `Process()`, `shouldRewrite()`, `rewriteTexts()`, `rewriteText()`, `executionUserPath()`, `appendGuardrailUserPath()`, `validateGuardrailPathSegment()`, `wrapAlteringText()`, `unwrapAlteredText()`: Execution flow wrapping an isolated Chat request to process the content dynamically without executing function calls.

### `./internal/guardrails/pipeline.go`

- `entry`, `Pipeline`, `result`: Organizes multiple `Guardrail` instances and defines order parameters allowing them to execute sequentially or concurrently.
- `NewPipeline()`, `Add()`, `Len()`, `groups()`, `Process()`, `runGroupParallel()`: Pipeline execution mechanism.

### `./internal/guardrails/planned_executor.go`

- `ContextPipelineResolver`, `PlannedRequestPatcher`, `PlannedBatchPreparer`: Ties Guardrails to `ExecutionPlans` context rules dynamically.
- `NewPlannedRequestPatcher()`, `PatchChatRequest()`, `PatchResponsesRequest()`, `pipeline()`, `NewPlannedBatchPreparer()`, `PrepareBatchRequest()`, `batchFileTransport()`: Dispatcher methods.

### `./internal/guardrails/provider.go`

- `GuardedProvider`, `Options`: Decorates a base `core.RoutableProvider` ensuring pipeline patches run right before upstream requests hit the network.
- `NewGuardedProvider()`, `NewGuardedProviderWithOptions()`, `Supports()`, `GetProviderType()`, `ModelCount()`, `NativeFileProviderTypes()`, `ChatCompletion()`, `StreamChatCompletion()`, `ListModels()`, `Embeddings()`, `Responses()`, `StreamResponses()`, `nativeBatchRouter()`, `nativeBatchHintRouter()`, `nativeFileRouter()`, `batchFileTransport()`, `passthroughRouter()`, `rewriteGuardedChatBatchBody()`, `patchGuardedChatBatchBody()`, `patchChatMessagesJSON()`, `patchRawChatMessage()`, `rewriteGuardedResponsesBatchBody()`, `jsonFieldPatch`, `patchJSONObjectFields()`, `unmarshalJSONArray()`, `isZeroJSONFieldValue()`, `CreateBatch()`, `CreateBatchWithHints()`, `GetBatch()`, `ListBatches()`, `CancelBatch()`, `GetBatchResults()`, `GetBatchResultsWithHints()`, `ClearBatchResultHints()`, `CreateFile()`, `ListFiles()`, `GetFile()`, `DeleteFile()`, `GetFileContent()`, `recordBatchPreparation()`, `cleanupSupersededBatchRewriteFile()`, `cleanupBatchRewriteFile()`, `mergeBatchHints()`, `Passthrough()`, `PatchChatRequest()`, `PatchResponsesRequest()`, `PrepareBatchRequest()`, `chatToMessages()`, `applyMessagesToChatPreservingEnvelope()`, `tailMatchedSystemOffsets()`, `applyGuardedMessageToOriginal()`, `newChatMessageFromGuardrail()`, `applyGuardedContentToOriginal()`, `rewriteStructuredContentWithTextRewrite()`, `normalizeGuardrailMessageText()`, `responsesToMessages()`, `responsesInputToMessages()`, `coerceResponsesInputElements()`, `responsesInputElementToGuardrailMessage()`, `stringifyResponsesValue()`, `applyMessagesToResponses()`, `applyMessagesToResponsesInput()`, `applyMessagesToResponsesElements()`, `applyGuardedResponsesElementToOriginal()`, `applyGuardedResponsesContentToOriginal()`, `patchResponsesInputEnvelope()`, `patchResponsesInputMapSlice()`, `patchResponsesInputInterfaceSlice()`, `patchResponsesInputInterfaceElement()`, `patchResponsesInputMap()`, `restoreResponsesInputOutputValue()`, `responsesInputElementAsMap()`, `responsesInputElementAsAny()`, `isResponsesStructuredContent()`, `rewriteStructuredResponsesContentWithTextRewrite()`, `rewriteStructuredResponsesTypedContentParts()`, `rewriteStructuredResponsesInterfaceContentParts()`, `rewriteStructuredResponsesMapContentParts()`, `isResponsesTextPartType()`, `cloneResponsesInterfaceParts()`, `cloneResponsesInterfacePart()`, `cloneStringAnyMap()`, `cloneToolCalls()`, `cloneChatMessageEnvelope()`, `cloneMessageContent()`, `cloneContentParts()`, `cloneContentPart()`: Wrappers tracking, caching, and reconstructing all varied JSON representations safely modifying payloads.

### `./internal/guardrails/registry.go`

- `StepReference`, `registryEntry`, `Registry`: Associates raw names with compiled instances to fulfill Execution Plans requests.
- `NewRegistry()`, `Len()`, `Names()`, `Register()`, `BuildPipeline()`: Registry tracking logic.

### `./internal/guardrails/service.go`

- `serviceSnapshot`, `Service`: Core API.
- `NewService()`, `Refresh()`, `SetExecutor()`, `refreshLocked()`, `UpsertDefinitions()`, `List()`, `ListViews()`, `Get()`, `Upsert()`, `Delete()`, `TypeDefinitions()`, `Len()`, `Names()`, `BuildPipeline()`, `buildSnapshot()`, `definitionMap()`, `definitionsFromMap()`, `guardrailServiceError()`: API methods matching standard module layouts.

### `./internal/guardrails/store.go`

- `ErrNotFound`, `ValidationError`: Errors.
- `IsValidationError()`, `newValidationError()`, `Store`, `definitionScanner`, `definitionRows`: Interface types.
- `normalizeDefinitionName()`, `collectDefinitions()`, `nullableString()`, `nullableStringValue()`: Formatters.

### `./internal/guardrails/store_mongodb.go`

- DB Implementations (`MongoDBStore`).

### `./internal/guardrails/store_postgresql.go`

- DB Implementations (`PostgreSQLStore`).

### `./internal/guardrails/store_sqlite.go`

- DB Implementations (`SQLiteStore`).

### `./internal/guardrails/system_prompt.go`

- `SystemPromptMode`, `SystemPromptGuardrail`: Appends, replaces, or wraps the System Prompt to enforce rules.
- `NewSystemPromptGuardrail()`, `effectiveSystemPromptMode()`, `isValidSystemPromptMode()`, `Name()`, `Process()`, `inject()`, `override()`, `decorate()`: Message processing logic.

---

## `./internal/httpclient/`

It implements tuned networking protocols.

### `./internal/httpclient/client.go`

- `ClientConfig`: Settings for timeouts and pooling.
- `getEnvDuration()`, `DefaultConfig()`, `NewHTTPClient()`, `NewDefaultHTTPClient()`: Configuration logic ensuring aggressive read/header timeouts missing from the standard `http.DefaultClient`.

---

## `./internal/llmclient/`

It wraps `net/http` to provide advanced resilience logic.

### `./internal/llmclient/client.go`

- `RequestInfo`, `ResponseInfo`, `Hooks`, `Config`, `HeaderSetter`: Configurations identifying requests for metrics monitoring.
- `Client`: HTTP Controller.
- `DefaultConfig()`, `New()`, `NewWithHTTPClient()`, `SetBaseURL()`, `BaseURL()`, `getBaseURL()`: Setup logic.
- `Request`, `Response`, `requestScope`: Datatypes capturing raw byte streams.
- `beginRequest()`, `finishRequest()`, `recordCircuitBreakerCompletion()`, `shouldTripCircuitBreaker()`, `maxAttempts()`, `waitForRetry()`: Handles Backoffs, Jitters, and Hooks.
- `Do()`, `DoRaw()`, `DoStream()`, `DoPassthrough()`: Core network routines resolving errors via Core parsing logic.
- `canRetryPassthrough()`, `hasIdempotencyKey()`, `extractModel()`, `extractStatusCode()`, `doHTTPRequest()`, `doRequest()`, `buildRequest()`, `calculateBackoff()`, `isRetryable()`: Request construction and verification.
- `circuitBreaker`, `circuitState`: Finite state machine protecting instances from spamming overloaded provider APIs.
- `newCircuitBreaker()`, `acquire()`, `Allow()`, `RecordSuccess()`, `RecordFailure()`, `State()`, `IsHalfOpen()`: Tracking logic.

---

## `./internal/modeldata/`

It handles scraping public AI model metadata (Pricing, Max Tokens) to enrich requests context locally.

### `./internal/modeldata/enricher.go`

- `ModelInfoAccessor`, `EnrichStats`: Tools to merge new data.
- `Enrich()`: Applies the fetched models into the internal Provider Registry.

### `./internal/modeldata/fetcher.go`

- `Fetch()`: HTTP call pulling the `ai-model-list` definition file.
- `Parse()`: Deserializes the JSON.

### `./internal/modeldata/merger.go`

- `Resolve()`: Merges provider-specific definitions with underlying standard definitions.
- `resolveEntries()`, `resolveDirect()`, `resolveReverseProviderModelID()`, `resolveAlias()`, `selectAliasModelRef()`, `aliasTargetScore()`, `resolveModelRef()`, `buildMetadata()`, `buildRankings()`: Fallback resolution to locate model pricing rules properly mapping customized variants correctly.

### `./internal/modeldata/types.go`

- `ModelList`, `aliasTarget`, `ProviderEntry`, `ModelEntry`, `ProviderModelEntry`, `ParameterSpec`, `RankingEntry`, `AuthConfig`, `Modalities`, `RateLimits`: The schema mirroring the JSON payload describing the AI industry.
- `buildReverseIndex()`, `addAliasTarget()`, `splitProviderQualifiedAlias()`, `actualModelID()`: Indexes data for rapid evaluation.

---

## `./internal/modeloverrides/`

It selectively masks access to downstream models based on organizational policies.

### `./internal/modeloverrides/batch_preparer.go`

- `selectorResolver`, `BatchPreparer`: Iterates JSONL batch files, halting execution if the files attempt to invoke models the user's `UserPath` API key lacks permission to access.
- `NewBatchPreparer()`, `PrepareBatchRequest()`, `batchFileTransport()`, `resolveSelector()`, `requestedSelectorForDecodedRequest()`.

### `./internal/modeloverrides/factory.go`

- `Result`, `Close()`, `New()`, `NewWithSharedStorage()`, `newResult()`, `createStore()`: Standard Module Initialization.

### `./internal/modeloverrides/service.go`

- `compiledOverride`, `snapshot`: Memory map for rapid `O(1)` routing.
- `Service`: Main control mechanism.
- `NewService()`, `EnabledByDefault()`, `Refresh()`, `refreshLocked()`, `snapshot()`, `buildSnapshot()`, `cloneOverride()`, `snapshotOverrides()`, `upsertOverride()`, `deleteOverride()`, `rollbackContext()`, `List()`, `ListViews()`, `Get()`, `Upsert()`, `Delete()`, `StartBackgroundRefresh()`, `EffectiveState()`, `AllowsModel()`, `ValidateModelAccess()`, `FilterPublicModels()`, `effectiveState()`, `userPathAllowed()`: Operations mapping requests paths to accessibility trees ensuring locked resources are successfully blocked in endpoints and API catalogs.

### `./internal/modeloverrides/store.go`

- `ErrNotFound`, `ValidationError`, `IsValidationError()`, `newValidationError()`, `Store`, `collectOverrides()`: Abstractions.

### `./internal/modeloverrides/store_mongodb.go`, `store_postgresql.go`, `store_sqlite.go`

- Implementations of the SQL/Mongo queries storing rule mappings.

### `./internal/modeloverrides/types.go`

- `Override`, `ScopeKind`, `View`, `EffectiveState`, `Catalog`: Target model structs.
- `ScopeModel`, `ScopeProvider`, `ScopeProviderModel`: Hierarchy logic evaluating which policy overrides others.
- `ScopeKind()`, `normalizeOverrideInput()`, `normalizeStoredOverride()`, `normalizeSelectorInput()`, `selectorProviderNames()`, `normalizeUserPaths()`, `selectorString()`, `exactMatchKey()`, `splitFirst()`, `parseStoredSelectorParts()`, `cloneEnabled()`: Sanitization algorithms.

---

## `./internal/observability/`

It collects metrics on routing behavior.

### `./internal/observability/metrics.go`

- `RequestsTotal`, `RequestDuration`, `InFlightRequests`: Global prometheus gauges representing traffic shapes across different providers, endpoints, and statuses.
- `NewPrometheusHooks()`: A closure passed to `llmclient` that increments/decrements these stats accurately mapping network durations into buckets.
- `PrometheusMetrics`, `GetMetrics()`, `ResetMetrics()`, `HealthCheck()`: Operational handlers.

---

## `./internal/providers/`

It defines interfaces mapping HTTP providers seamlessly unifying logic mapping between Anthropic, Gemini, OpenAI, etc.

### `./internal/providers/batch_results_file_adapter.go`

- `openAICompatibleBatchLine`, `openAICompatibleRawRequester`, `openAICompatiblePassthroughRequester`, `openAICompatibleRequestPreparer`: Functions and types for interacting with generic OpenAI compatible Batch result files.
- `FetchBatchResultsFromOutputFile()`, `FetchBatchResultsFromOutputFileWithPreparer()`, `fetchBatchResultsFromOpenAICompatibleEndpoints()`, `normalizeOpenAICompatibleEndpointPrefix()`, `parseBatchFileMetadata()`, `isPendingBatchStatus()`, `parseBatchOutputFile()`, `ResponseURL()`, `firstString()`: Helper pulling down `.jsonl` files storing API responses via the generic OpenAI files endpoints mapping its payload to `core.BatchResultsResponse`.

### `./internal/providers/config.go`

- `ProviderConfig`: Merged model setting configurations.
- `resolveProviders()`, `applyProviderEnvVars()`, `findEnvOverlayTarget()`, `rawProviderMatchesType()`, `providerEnvNames`, `derivedEnvNames()`, `envPrefix()`, `sortedDiscoveryTypes()`, `normalizeResolvedBaseURL()`, `isUnresolvedEnvPlaceholder()`, `filterEmptyProviders()`, `buildProviderConfigs()`, `buildProviderConfig()`: Injects environmental configuration into API targets enabling automatic discovery (e.g. matching `OPENAI_API_KEY` dynamically creating the module).

### `./internal/providers/factory.go`

- `ProviderOptions`, `ProviderConstructor`, `DiscoveryConfig`, `Registration`: Settings mapping the name of a string in yaml (e.g. `azure`) to a specific constructor implementing the interface mapping rules.
- `ProviderFactory`: Map linking strings to Constructors dynamically.
- `NewProviderFactory()`, `SetHooks()`, `Add()`, `Create()`, `discoveryConfigsSnapshot()`, `RegisteredTypes()`, `PassthroughSemanticEnrichers()`: Methods bootstrapping providers safely passing the HTTP client configuration downwards.

### `./internal/providers/file_adapter_openai_compat.go`

- `validatedOpenAICompatibleFileID()`, `openAICompatibleRequestPreparer`, `prepareOpenAICompatibleRequest()`, `doOpenAICompatibleFileIDRequest()`, `doOpenAICompatibleFileIDRequestWithPreparer()`, `CreateOpenAICompatibleFile()`, `CreateOpenAICompatibleFileWithPreparer()`, `ListOpenAICompatibleFiles()`, `ListOpenAICompatibleFilesWithPreparer()`, `GetOpenAICompatibleFile()`, `GetOpenAICompatibleFileWithPreparer()`, `DeleteOpenAICompatibleFile()`, `DeleteOpenAICompatibleFileWithPreparer()`, `GetOpenAICompatibleFileContent()`, `GetOpenAICompatibleFileContentWithPreparer()`: Methods mapping `core.NativeFileProvider` over any service matching standard OpenAI API interfaces avoiding redundant code.

### `./internal/providers/init.go`

- `InitResult`: Wrapper carrying the Registry and background fetcher context.
- `Close()`, `Init()`, `initCache()`, `initializeProviders()`: Central wiring initializing providers, mapping aliases, triggering background network scans mapping capabilities (pricing data).

### `./internal/providers/passthrough.go`

- `PassthroughEndpoint()`, `CloneHTTPHeaders()`, `PassthroughEndpointPath()`: Utilities converting internal contexts safely into generic HTTP Proxy data.

### `./internal/providers/registry.go`

- `ModelInfo`, `ModelRegistry`, `metadataEnrichmentStats`: Tracks instances in memory efficiently enabling dynamic lookup tables based on incoming request properties.
- `slogAttrs()`, `NewModelRegistry()`, `SetCache()`, `invalidateSortedCaches()`, `RegisterProvider()`, `RegisterProviderWithType()`, `RegisterProviderWithNameAndType()`, `Initialize()`, `Refresh()`, `LoadFromCache()`, `SaveToCache()`, `InitializeAsync()`, `IsInitialized()`, `GetProvider()`, `GetModel()`, `LookupModel()`, `Supports()`, `ListModels()`, `ListPublicModels()`, `ModelCount()`, `GetProviderType()`, `GetProviderName()`, `GetProviderNameForType()`, `GetProviderTypeForName()`, `ProviderByType()`, `ProviderTypes()`, `ProviderNames()`, `splitModelSelector()`, `qualifyPublicModelID()`, `hasConfiguredProviderNameLocked()`, `ModelWithProvider`, `ListModelsWithProvider()`, `ListModelsWithProviderByCategory()`, `hasCategory()`, `CategoryCount`, `GetCategoryCounts()`, `ProviderCount()`, `SetModelList()`, `EnrichModels()`, `enrichModels()`, `ResolveMetadata()`, `GetModelMetadata()`, `ResolvePricing()`, `snapshotProviderTypes()`, `enrichProviderModelMaps()`, `registryAccessor`, `ModelIDs()`, `GetProviderType()`, `SetMetadata()`, `StartBackgroundRefresh()`, `refreshModelList()`, `isBenignBackgroundRefreshError()`: Comprehensive storage indexing models and metadata efficiently.

### `./internal/providers/resolved_config.go`

- `ResolveBaseURL()`, `ResolveAPIVersion()`: Small normalizers checking default value strings.

### `./internal/providers/responses_adapter.go`

- `ChatProvider`: Abstract implementation requirement.
- `ConvertResponsesRequestToChat()`, `cloneStreamOptions()`, `normalizeResponsesToolsForChat()`, `normalizeResponsesToolForChat()`, `normalizeResponsesToolChoiceForChat()`, `cloneStringAnyMap()`, `ConvertResponsesInputToMessages()`, `convertResponsesInputItems()`, `convertResponsesInputItem()`, `convertResponsesInputElement()`, `convertResponsesInputMap()`, `cloneResponsesMessage()`, `canMergeAssistantMessages()`, `mergeAssistantMessage()`, `isAssistantToolCallOnlyMessage()`, `ConvertResponsesContentToChatContent()`, `convertResponsesContentParts()`, `normalizeTypedResponsesContentPart()`, `finalizeResponsesChatContent()`, `canFlattenResponsesPartsToText()`, `normalizeResponsesImageURLForChat()`, `normalizeResponsesInputAudioForChat()`, `cloneResponsesToolCall()`, `cloneResponsesContentPart()`, `rawJSONMapFromUnknownKeys()`, `rawJSONMapFromUnknownStringKeys()`, `firstNonEmptyString()`, `stringifyResponsesInputValue()`, `stringifyResponsesInputValueWithError()`, `ExtractContentFromInput()`, `extractTextFromMapSlice()`, `extractTextFromInputMap()`, `ResponsesFunctionCallCallID()`, `ResponsesFunctionCallItemID()`, `buildResponsesMessageContent()`, `buildResponsesContentItemsFromParts()`, `BuildResponsesOutputItems()`, `ConvertChatResponseToResponses()`, `ResponsesViaChat()`, `StreamResponsesViaChat()`: Extensively converts advanced multimodal JSON properties between the standard `Chat` structure and the specialized newer OpenAI `Responses` format. Converts logic cleanly for models not natively implementing Responses.

### `./internal/providers/responses_converter.go`

- `OpenAIResponsesStreamConverter`: Scans an active stream capturing bytes evaluating Server-Sent Events extracting their JSON data adapting it between Chat streams and Responses streams mapping delta segments over tool outputs generating unified Responses streams output mapping state gracefully.
- `NewOpenAIResponsesStreamConverter()`, `normalizeToolCallIndex()`, `ensureToolCallState()`, `reserveAssistantOutput()`, `forceStartToolCall()`, `completePendingToolCalls()`, `handleToolCallDeltas()`, `Read()`, `Close()`: Transforms streams asynchronously.

### `./internal/providers/responses_done_wrapper.go`

- `EnsureResponsesDone()`, `responsesDoneWrapper`: Ensures stream completeness intercepting IO Read mapping standard event limits injecting missing EOF tags cleanly allowing middleware caching limits to capture valid structures cleanly preserving connection closures without data loss.
- `Read()`, `trackStream()`, `processLine()`, `synthesizeDoneSuffix()`, `isDoneLinePrefix()`, `isCompletedDataLine()`: Parser boundaries.

### `./internal/providers/responses_output_state.go`

- `ResponsesOutputToolCallState`, `ResponsesOutputEventState`: Models holding chunks of text parsing functional inputs safely appending incomplete tool arguments correctly rendering output properties mapping partial states accurately generating exact responses cleanly.
- `NewResponsesOutputEventState()`, `WriteEvent()`, `ReserveAssistant()`, `AssistantReserved()`, `AssistantStarted()`, `AssistantDone()`, `AppendAssistantText()`, `AssistantMessageItem()`, `StartAssistantOutput()`, `CompleteAssistantOutput()`, `ToolCallArguments()`, `RenderToolCallItem()`, `StartToolCall()`, `CompleteToolCall()`: State machine logic ensuring event sequence validity.

### `./internal/providers/router.go`

- `ErrRegistryNotInitialized`: Standard error.
- `Router`: Maps inbound model strings (e.g. `gpt-5`) into executing Provider instances cleanly verifying fallback defaults mapping missing targets effectively rejecting unknown items safely verifying execution cleanly.
- `providerTypeRegistry`, `initializedLookup`, `providerTypeLister`, `providerNameLister`, `publicModelLister`, `modelWithProviderLister`, `registryUnavailableError()`, `NewRouter()`, `checkReady()`, `ResolveModel()`, `resolveUnqualifiedSelector()`, `resolveQualifiedSelector()`, `hasConfiguredProviderName()`, `resolveProvider()`, `resolveProviderType()`, `resolveNativeBatchProvider()`, `resolveNativeFileProvider()`, `resolvePassthroughProvider()`, `routeResolvedModelCall()`, `routeStampedModelResponse()`, `routeNativeBatchCall()`, `routeNativeFileCall()`, `stampProvider()`, `forwardChatRequest()`, `forwardResponsesRequest()`, `forwardEmbeddingRequest()`, `callChatCompletion()`, `callResponses()`, `callEmbeddings()`, `Supports()`, `ModelCount()`, `ChatCompletion()`, `StreamChatCompletion()`, `ListModels()`, `Responses()`, `StreamResponses()`, `Embeddings()`, `GetProviderType()`, `GetProviderName()`, `GetProviderNameForType()`, `GetProviderTypeForName()`, `providerByType()`, `providerByTypeRegistry()`, `providerTypes()`, `NativeFileProviderTypes()`, `Passthrough()`, `CreateBatch()`, `CreateBatchWithHints()`, `GetBatch()`, `ListBatches()`, `CancelBatch()`, `GetBatchResults()`, `GetBatchResultsWithHints()`, `ClearBatchResultHints()`, `CreateFile()`, `ListFiles()`, `GetFile()`, `DeleteFile()`, `GetFileContent()`: Exposes `core.RoutableProvider` and dynamically matches any downstream provider supporting native implementation calls cleanly.

---

### Specific Providers (`internal/providers/<vendor>/`)

**`anthropic`**:

- `anthropic.go`: `Registration`, `Provider` implementing `ChatCompletion`, `Responses` mapping tools translating schemas correctly extracting parameters correctly managing batch mapping endpoints explicitly evaluating context limits caching rules natively.
- `passthrough_semantics.go`: Enriches audit logging tagging native URLs safely matching schemas cleanly mapping metrics successfully exposing explicit data structures properly.
- `request_translation.go`: Re-formats `ToolCall` and System limits fixing constraints modifying inputs enabling native formatting without data loss processing images accurately.

**`azure`**:

- `azure.go`: `Provider`, `New()` wrapping OpenAI configuration modifying endpoints dynamically updating API queries generating precise resource identifiers seamlessly proxying endpoints efficiently utilizing native open ai components transparently.

**`gemini`**:

- `gemini.go`: `Provider`, `New()`, `ChatCompletion()`, `ListModels()` converting schemas natively translating errors extracting capabilities evaluating models ensuring valid identifiers resolving names directly hitting endpoints natively utilizing google apis mapping endpoints correctly handling streams seamlessly parsing errors explicitly routing calls cleanly.

**`groq`**:

- `groq.go`: Standard OpenAI proxy configuring client executing requests.

**`ollama`**:

- `ollama.go`: Wrapper hitting Local endpoints handling non-standard metadata extracting native formats cleanly passing through parameters executing embeddings natively resolving errors properly skipping batch functionalities gracefully.

**`openai`**:

- `openai.go`: Wraps the compatible interface injecting standard parameters mapping environments securely defining models natively filtering logic appropriately parsing variables flawlessly ensuring execution correctly formatting requests seamlessly processing streams properly handling endpoints accurately logging outputs reliably.
- `compatible_provider.go`: Generic wrapper mapping all internal provider functions directly into LLM SDK wrappers generating output dynamically validating inputs effectively handling connections flawlessly mapping structures properly processing data securely ensuring responses reliably formatting streams efficiently executing operations precisely handling batch APIs parsing files processing errors verifying properties handling requests gracefully.
- `passthrough_semantics.go`: Enriches semantics matching URLs properly determining endpoints accurately exposing data appropriately modifying streams properly identifying features directly enriching data.

**`openrouter`**:

- `openrouter.go`: Injects HTTP Referer and Site Title tracking metadata correctly forwarding open ai formatted requests natively mapping data explicitly extending defaults cleanly applying variables securely generating requests effectively resolving features appropriately updating connections mapping models configuring endpoints mapping logic handling configuration.

**`oracle`**:

- `oracle.go`: Specific handler fixing missing API formats utilizing configured lists avoiding unsupported features returning accurate errors handling models directly processing features returning defaults updating state handling endpoints efficiently mapping execution cleanly formatting environments appropriately exposing tools securely generating properties.

**`xai`**:

- `xai.go`: Grok implementation mapping open ai tools hitting correct endpoints routing tokens configuring models formatting variables mapping environments defining data parsing inputs updating rules identifying structures.

---

## `./internal/responsecache/`

It accelerates inference significantly returning cached results.

### `./internal/responsecache/responsecache.go`

- `ResponseCacheMiddleware`: Combines both caching mechanisms securely wrapping the HTTP echo controller intercepting streams evaluating logic efficiently skipping cached outputs writing values seamlessly.
- `InternalHandleResult`, `NewResponseCacheMiddleware()`, `Middleware()`, `HandleRequest()`, `HandleInternalRequest()`, `Close()`, `internalRequestHeaders()`, `internalCacheType()`, `NewResponseCacheMiddlewareWithStore()`: Sets up the memory state checking rules validating configurations verifying structures mapping outputs evaluating settings.

### `./internal/responsecache/semantic.go`

- `semanticCacheMiddleware`: Evaluates prompt vector similarity bypassing inference entirely loading cached representations reconstructing full streaming outputs parsing inputs properly executing rules safely returning structures validating context correctly evaluating parameters extracting tokens defining schemas effectively.
- `embedMessage`, `extractEmbedText()`, `embedTextFromMessages()`, `conversationInvariantFingerprint()`, `messageRawListFromMessagesOrInput()`, `writeNonTextUserContentFingerprint()`, `extractTextFromContent()`, `computeParamsHash()`, `sortedTools()`, `shouldSkipSemanticCache()`, `headerFloat64()`, `headerDuration()`, `sha256HexOf()`, `ComputeGuardrailsHash()`, `GuardrailRuleDescriptor`, `GuardrailsHashFromContext()`, `WithGuardrailsHash()`, `ShouldSkipExactCache()`, `ShouldSkipAllCache()`, `IoReadAllBody()`: Generates unique context fingerprints defining bounds calculating similarity formatting streams parsing embeddings mapping text extracting properties comparing schemas evaluating states ensuring validity matching fields updating contexts modifying logic accurately validating structures handling values cleanly processing rules safely mapping conditions directly defining keys evaluating inputs executing logic handling states securely converting structures explicitly generating identifiers tracking contexts extracting bytes accurately preserving constraints verifying inputs validating configurations applying keys processing data.

### `./internal/responsecache/simple.go`

- `cacheWriteJob`, `simpleCacheMiddleware`: Memory store matching exact SHA256 hashes generating cache hits instantaneously mapping responses verifying options caching streams intercepting headers updating bytes correctly handling memory cleanly.
- `newSimpleCacheMiddleware()`, `Middleware()`, `TryHit()`, `StoreAfter()`, `close()`, `startWorkers()`, `enqueueWrite()`, `shouldSkipCacheForExecutionPlan()`, `requestBodyForCache()`, `shouldSkipCache()`, `isStreamingRequest()`, `isStreamingRequestGJSON()`, `hashRequest()`, `responseCapture`: High throughput writer buffer intercepting responses returning streams properly writing properties extracting configurations applying limits parsing contexts ensuring bounds validating contents updating data handling objects identifying states copying properties mapping output returning properties configuring states handling variables properly evaluating logic.

### `./internal/responsecache/sse_validation.go`

- `validateCacheableSSE()`, `sseEventPayload()`: Validates stream chunks preventing partial caching corruptions checking event strings parsing payload boundaries mapping blocks decoding logic verifying strings returning status extracting boundaries parsing fragments extracting properties mapping rules checking chunks matching structures evaluating states.

### `./internal/responsecache/stream_cache.go`

- `streamResponseDefaults`, `chatToolCallState`, `chatChoiceState`, `chatStreamCacheBuilder`, `responsesOutputState`, `responsesStreamCacheBuilder`: Maintains complex state maps reconstructing JSON structures across Server Sent Events mapping parts decoding structures updating fragments merging blocks modifying data defining schemas generating values mapping contexts checking inputs applying formats formatting components configuring states generating objects evaluating fragments extracting elements returning content.
- `cacheKeyRequestBody()`, `isEventStreamContentType()`, `writeCachedResponse()`, `cacheHeaderValue()`, `renderCachedChatStream()`, `renderCachedResponsesStream()`, `reconstructStreamingResponse()`, `parseSSEJSONEvents()`, `parseCacheEventJSON()`, `nextCacheEventBoundary()`, `parseCacheDataLine()`, `OnJSONEvent()`, `Build()`, `choice()`, `toolCall()`, `captureResponseMetadata()`, `output()`, `lookupOutputIndex()`, `rememberOutputLocator()`, `ensureReasoningOutputIndex()`, `SetItem()`, `AppendText()`, `AppendReasoning()`, `AppendArguments()`, `SetArguments()`, `textPart()`, `reasoningPart()`, `BuildItem()`, `buildResponsesContentParts()`, `cloneJSONPart()`, `buildChatToolCalls()`, `renderChatToolCalls()`, `normalizeStreamOptionsForCache()`, `streamIncludeUsageRequested()`, `chatReasoningContent()`, `responsesAddedItem()`, `responsesTerminalEventName()`, `appendResponsesItemDeltaEvents()`, `responsesContentDeltaEvent()`, `chatUsageMap()`, `appendSSEJSONEvent()`, `toJSONMap()`, `cloneJSONMap()`, `jsonNumberToInt()`, `jsonNumberToInt64()`, `nonEmpty()`: Data extraction and formatting.

### `./internal/responsecache/usage_hit.go`

- `newUsageHitRecorder()`: Callback injecting usage events reporting saved tokens capturing context generating output evaluating pricing mapping components storing states reporting values accurately configuring metrics parsing limits extracting costs generating logs creating structures tracking variables maintaining states applying rules handling properties tracking usage defining logs handling variables generating items parsing records tracking contexts.

### `./internal/responsecache/vecstore*.go`

- `VecResult`, `VecStore`: Interfaces providing vector matching returning similar content identifying components tracking bounds comparing distances storing properties measuring fields returning queries identifying similarities evaluating structures processing elements matching vectors executing searches defining limits setting boundaries checking components locating states finding features returning variables returning lists parsing fields creating matches evaluating conditions validating structures comparing fields defining values identifying contexts mapping parameters parsing schemas returning objects tracking boundaries finding logic configuring properties searching components extracting variables saving parameters parsing objects.
- `NewVecStore()`: Constructor.
- `vecCleanup`: Background struct managing expirations parsing dates applying logic processing elements.
- `startVecCleanup()`, `close()`, `vecPointID64()`, `pgvectorLiteral()`, `trimSlash()`: Helpers configuring settings.
- `MapVecStore` (`vecstore_map.go`): In-memory linear search comparing cosine values matching fields returning scores parsing vectors updating indexes applying formats parsing limits managing fields extracting elements evaluating limits identifying vectors processing values checking distances calculating logic returning bounds maintaining loops extracting elements measuring metrics creating distances.
- `pgVecStore` (`vecstore_pgvector.go`): PostgreSQL `vector` extension defining schemas mapping parameters extracting bounds defining logic matching tables returning limits applying boundaries evaluating queries configuring properties selecting logic indexing data comparing ranges connecting states identifying distances sorting results updating variables tracking elements creating indexes validating constraints modifying contexts comparing distances checking values returning items measuring thresholds.
- `pineconeStore` (`vecstore_pinecone.go`): Native cloud Pinecone integration querying remote clusters extracting arrays converting strings comparing values fetching URLs mapping variables validating environments establishing connections verifying endpoints passing headers formatting JSON applying namespaces parsing responses verifying structures updating variables retrieving data setting parameters checking boundaries processing matches evaluating values modifying fields extracting contexts creating objects encoding bytes comparing fields returning endpoints mapping constraints identifying values measuring vectors comparing logic mapping keys.
- `qdrantStore` (`vecstore_qdrant.go`): Qdrant DB proxy extracting points defining boundaries modifying structures formatting bytes sending connections applying schemas decoding states processing payloads capturing results comparing values fetching logic creating requests setting parameters mapping components establishing properties extracting distances parsing filters formatting queries handling options modifying dimensions comparing scores reading fields defining filters retrieving elements evaluating points checking limits defining contexts handling bytes creating objects storing items processing logic extracting matches defining constraints converting components generating requests identifying limits capturing elements.
- `weaviateStore` (`vecstore_weaviate.go`): Weaviate GraphQL mapping extracting elements identifying endpoints fetching responses decoding schemas tracking classes matching keys validating structures processing logic updating values tracking variables comparing variables parsing connections generating structures applying settings finding configurations setting formats mapping bounds evaluating configurations checking conditions modifying limits passing contexts returning parameters checking options executing properties maintaining contexts.

---

## `./internal/server/`

It holds the main Echo HTTP web server logic mapping context processing chains executing API structures resolving configurations maintaining states returning endpoints capturing metrics parsing routes injecting components checking boundaries measuring structures verifying fields evaluating queries checking configurations.

### `./internal/server/auth.go`

- `BearerTokenAuthenticator`: Interface determining valid API tokens checking keys maintaining status executing rules fetching properties parsing states defining variables mapping parameters identifying endpoints tracking requests returning flags matching rules extracting components checking configurations extracting elements storing properties executing values tracking rules decoding configurations matching boundaries.
- `AuthMiddleware()`, `AuthMiddlewareWithAuthenticator()`: Intercepting functions authenticating headers stripping values validating secrets defining states handling roles returning errors extracting paths matching rules verifying elements modifying paths fetching headers checking formats setting contexts formatting paths rejecting combinations handling headers executing contexts passing properties recording states managing elements parsing fields mapping errors.
- `authFailureMessage()`, `authenticationError()`, `authenticationErrorWithAudit()`: Error generators creating JSON returning faults generating logic assigning values defining schemas determining formats returning properties maintaining limits processing combinations building types setting contexts recording formats logging metrics checking rules formatting boundaries generating keys matching errors processing structures capturing variables handling items tracking failures defining fields mapping logic extracting limits parsing values returning responses updating headers mapping paths mapping elements defining states establishing constants returning conditions checking paths modifying components.

### `./internal/server/batch_request_preparer.go`

- `BatchRequestPreparer`: Interface handling asynchronous items mapping functions applying changes processing objects defining rules checking values processing elements configuring fields creating rules mapping boundaries verifying components processing states applying items generating lists storing data evaluating elements tracking bounds checking files defining limits tracking requests mapping contexts fetching variables executing fields formatting bounds maintaining objects parsing fields checking options maintaining values handling data processing parameters modifying states evaluating responses creating queries checking paths extracting settings identifying conditions mapping logic returning elements.
- `batchRequestPreparerChain`: Iterating wrapper combining interfaces defining layers routing combinations building paths processing steps creating variables passing loops defining conditions assigning values processing outputs identifying formats evaluating layers converting limits recording states tracking bounds checking files saving fields executing structures formatting logic.
- `ComposeBatchRequestPreparers()`, `PrepareBatchRequest()`, `cleanupBatchRewriteFile()`: Constructor executing steps returning output mapping responses comparing contexts checking values tracking options handling layers defining limits tracking limits maintaining lists finding paths handling inputs parsing errors defining boundaries executing functions tracking loops processing parameters logging contexts recording states modifying outputs defining operations finding elements passing components filtering paths extracting limits managing bounds assigning variables comparing variables processing steps comparing loops generating errors establishing keys assigning layers determining limits parsing contexts.

### `./internal/server/error_support.go`

- `handleError()`: Converts gateway problems mapping JSON capturing components logging properties setting values fetching headers processing errors applying formats verifying strings recording bounds returning elements identifying metrics establishing responses handling contexts converting items mapping objects formatting outputs defining rules defining contexts setting limits handling attributes saving formats tracking limits generating outputs mapping properties reporting failures.
- `logHandledError()`: Adds contextual dimensions injecting types recording attributes capturing inputs logging traces fetching contexts identifying boundaries recording values adding arguments comparing items mapping queries extracting bounds printing output setting states reading boundaries recording structures processing messages parsing contexts evaluating properties creating states comparing fields defining inputs generating limits logging values tracking elements formatting components establishing variables executing items finding limits finding components processing limits processing variables updating rules formatting keys extracting variables handling responses tracking inputs checking operations processing formats defining messages printing rules.

### `./internal/server/execution_plan_helpers.go`

- `ensureTranslatedRequestPlan()`, `ensureTranslatedRequestPlanWithAuthorizer()`, `ensureTranslatedExecutionPlan()`, `currentTranslatedExecutionPlan()`, `translatedPlanResolution()`, `applyResolvedSelector()`, `translatedExecutionPlanForRequest()`, `translatedExecutionPlan()`: Resolves logic chains executing workflows determining fallback tracking components mapping features handling scopes routing elements applying constraints mapping aliases tracking environments generating endpoints evaluating models defining scopes checking keys recording policies extracting paths matching environments verifying properties generating boundaries fetching parameters mapping rules tracking models determining operations finding limits verifying structures formatting options capturing contexts identifying endpoints parsing constraints finding variables updating rules applying options executing variables creating contexts logging attributes.

### `./internal/server/execution_policy.go`

- `RequestExecutionPolicyResolver`: Matches logic mapping constraints.
- `applyExecutionPolicy()`, `applyExecutionContextOverrides()`, `normalizeExecutionPolicyError()`, `cloneCurrentExecutionPlan()`, `executionPlanVersionID()`, `boolPtr()`: Injects features setting guardrails processing flags turning off functions extracting variables logging objects handling boundaries finding logic verifying properties returning defaults identifying components checking inputs generating keys formatting properties configuring states passing attributes returning structures managing variables checking structures verifying limits parsing components extracting fields establishing references creating states formatting logic applying variables handling loops evaluating fields defining scopes identifying structures passing outputs logging flags handling limits tracking operations determining rules extracting contexts setting options mapping values.

### `./internal/server/exposed_model_lister.go`

- `ExposedModelLister`, `FilteredExposedModelLister`: Abstract listing properties extracting definitions creating structures defining paths filtering keys evaluating rules executing logic parsing arrays handling options recording limits returning boundaries copying limits generating types.
- `mergeExposedModelsResponse()`: Merging responses combining arrays removing duplicates establishing maps sorting objects assigning attributes updating lists formatting responses setting contexts handling fields processing structures creating bounds parsing values adding components verifying maps checking inputs handling combinations sorting logic matching structures passing limits maintaining logic generating elements extracting logic identifying loops measuring objects logging definitions checking configurations updating parameters.

### `./internal/server/handlers.go`

- `Handler`: Controller holding memory executing dependencies mapping pointers tracking logic parsing requests formatting schemas assigning references instantiating fields generating connections evaluating properties passing boundaries capturing operations returning responses determining states recording logs capturing elements setting options applying parameters identifying rules establishing bounds assigning interfaces loading metrics updating options updating environments determining limits configuring services allocating components updating components passing dependencies building handlers loading paths tracking parameters parsing responses mapping environments executing values.
- `NewHandler()`, `newHandler()`, `newHandlerWithAuthorizer()`, `SetBatchStore()`, `translatedInference()`, `nativeBatch()`, `nativeFiles()`, `passthrough()`, `ProviderPassthrough()`, `ChatCompletion()`, `Health()`, `ListModels()`, `CreateFile()`, `ListFiles()`, `GetFile()`, `DeleteFile()`, `GetFileContent()`, `Responses()`, `Embeddings()`, `Batches()`, `GetBatch()`, `ListBatches()`, `CancelBatch()`, `BatchResults()`: Exposes internal functions formatting HTTP routes determining bounds assigning objects capturing streams fetching logs writing responses capturing structures comparing rules processing fields validating contexts executing queries passing states returning errors generating values modifying requests processing schemas.

### `./internal/server/http.go`

- `Server`, `Config`: The central HTTP router wrapper configuration initializing variables starting tasks tracking options applying settings mapping parameters recording components building routes configuring elements setting ports identifying servers tracking attributes generating endpoints injecting references validating components logging rules maintaining contexts adding metrics parsing properties handling requests measuring states parsing environments storing attributes maintaining components fetching limits returning operations tracking responses capturing errors identifying bounds determining configurations parsing paths filtering endpoints returning loops fetching logic loading paths validating outputs assigning features returning limits tracking parameters.
- `New()`, `passthroughV1PrefixNormalizationEnabled()`, `Start()`, `Shutdown()`, `ServeHTTP()`, `newGatewayStartConfig()`, `configureGatewayHTTPServer()`, `modelInteractionWriteDeadlineMiddleware()`, `parseBodySizeLimitBytes()`: Framework initializers creating servers updating configurations processing boundaries defining types capturing connections terminating tasks closing handlers logging properties checking bounds filtering environments measuring structures running operations executing types finding limits applying limits parsing endpoints routing tasks formatting logic defining rules updating variables logging errors maintaining contexts finding parameters finding structures adding attributes identifying values evaluating properties extracting values tracking logic finding options generating bounds capturing operations mapping bounds determining values handling settings managing connections establishing rules identifying bounds checking paths logging fields identifying limits returning outputs running loops parsing strings setting contexts returning limits applying components tracking boundaries maintaining options validating configurations checking operations identifying environments.

### `./internal/server/internal_chat_completion_executor.go`

- `InternalChatCompletionExecutorConfig`, `InternalChatCompletionExecutor`: Helper structure instantiating isolated logic running endpoints modifying values checking paths extracting boundaries generating inputs returning responses establishing queries fetching limits executing tools maintaining contexts checking elements generating keys passing contexts applying parameters measuring rules building contexts identifying formats checking parameters finding limits formatting logic logging parameters processing environments executing variables routing queries tracking states determining scopes tracking parameters identifying limits formatting contexts mapping items returning properties evaluating boundaries logging operations extracting limits formatting components mapping limits executing components checking states determining types parsing strings returning outputs finding keys verifying combinations modifying outputs extracting operations checking rules.
- `NewInternalChatCompletionExecutor()`, `ChatCompletion()`, `executeChatCompletion()`, `newAuditEntry()`, `finishAuditEntry()`, `chatResponseModel()`: Interacts internally applying logic.

### `./internal/server/model_access.go`

- `RequestModelAuthorizer`: Determines access processing filters capturing roles measuring rules validating variables formatting properties generating conditions executing inputs mapping constraints handling rules identifying operations extracting options formatting configurations identifying paths managing elements identifying lists generating keys verifying limits filtering outputs capturing logic identifying combinations measuring limits.

### `./internal/server/model_validation.go`

- `modelCountProvider`: Determines limits verifying bounds validating rules.
- `ExecutionPlanning()`, `ExecutionPlanningWithResolver()`, `ExecutionPlanningWithResolverAndPolicy()`, `deriveExecutionPlanWithPolicy()`, `storeExecutionPlan()`, `selectorHintsForValidation()`, `cachedCanonicalSelectorHints()`, `selectorHintsFromJSONGJSON()`, `selectorHintValueAllowed()`, `providerPassthroughType()`, `passthroughRouteInfo()`, `GetProviderType()`: Middleware logic examining structures validating formats tracking boundaries generating models extracting operations maintaining paths extracting references checking parameters determining properties loading contexts storing components processing fields checking inputs formatting scopes applying limits extracting components identifying values building options maintaining options testing logic reading configurations generating parameters formatting keys measuring logic updating conditions adding parameters storing bounds identifying limits.

### `./internal/server/native_batch_service.go`

- `batchResultsPending404Providers`: Constant tracking environments handling options.
- `batchExecutionSelection`: Structure.
- `determineBatchExecutionSelection()`, `determineBatchExecutionSelectionWithAuthorizer()`, `mergeBatchRequestEndpointHints()`, `nativeBatchService`: Helper managing properties generating interfaces tracking logic assigning values routing objects handling lists building variables generating lists.
- `Batches()`, `rollbackPreparedBatch()`, `storeExecutionPlanForBatch()`, `clearUpstreamBatchResultHints()`, `cancelUpstreamBatch()`, `GetBatch()`, `ListBatches()`, `CancelBatch()`, `BatchResults()`, `requireStoredBatch()`, `batchIDFromRequest()`, `auditBatchEntry()`: Routes operations returning structures evaluating settings identifying bounds handling contexts formatting parameters handling arrays mapping logic tracking bounds validating structures handling inputs formatting fields tracking combinations parsing schemas extracting queries evaluating conditions generating bounds returning formats mapping models verifying boundaries matching conditions extracting structures modifying parameters determining settings evaluating items maintaining ranges returning loops updating objects passing conditions processing structures modifying types finding parameters identifying keys handling limits storing lists finding values determining strings managing paths handling configurations defining endpoints capturing values.

### `./internal/server/native_batch_support.go`

- `cleanupPreparedBatchInputFile()`, `loadBatch()`, `logBatchUsageFromBatchResults()`, `deterministicBatchUsageID()`, `buildBatchUsageRawData()`, `extractTokenTotals()`, `readFirstInt()`, `intFromAny()`, `intFromInt64()`, `intFromUint64()`, `intFromFloat64()`, `asJSONMap()`, `decodeJSONMap()`, `stringFromAny()`, `firstNonEmpty()`, `mergeStoredBatchFromUpstream()`, `hasBatchRequestCounts()`, `hasBatchUsageSummary()`, `isTerminalBatchStatus()`, `cleanupStoredBatchRewrittenInputFile()`, `isNativeBatchResultsPending()`: Execution logic extracting limits mapping formats reading values returning outputs executing boundaries testing options mapping variables formatting strings finding schemas identifying parameters generating constraints verifying components reading configurations generating queries extracting parameters managing operations executing components capturing keys loading lists mapping states converting fields handling environments setting objects logging values fetching paths identifying lists mapping numbers identifying contexts comparing variables mapping options finding limits logging limits processing inputs processing variables formatting structures determining rules tracking formats finding strings finding limits extracting limits formatting keys loading outputs logging strings capturing components filtering responses determining constraints reading structures matching operations evaluating structures tracking lists checking arrays defining options matching inputs verifying combinations handling bounds parsing variables mapping types identifying lists identifying strings managing logic mapping loops checking bounds tracking bounds measuring elements returning components generating properties measuring operations mapping logic verifying loops generating strings processing responses verifying ranges verifying configurations measuring rules capturing errors handling conditions capturing attributes finding operations returning objects tracking arrays identifying values.

### `./internal/server/native_file_service.go`

- `nativeFileService`: Maps files maintaining logic checking values extracting references routing options generating streams fetching content handling structures testing items.
- `router()`, `providerTypes()`, `fileByID()`, `CreateFile()`, `ListFiles()`, `GetFile()`, `DeleteFile()`, `GetFileContent()`, `isNotFoundGatewayError()`, `isUnsupportedNativeFilesError()`, `providerFileListState`, `listMergedFiles()`, `loadProviderFilePage()`, `nextMergedFile()`, `fileSortsBefore()`: Functions verifying constraints loading logic mapping fields handling responses retrieving parts reading attributes updating contexts reading sizes returning types checking contexts parsing streams formatting requests tracking formats defining logic processing boundaries creating elements managing types determining operations loading strings extracting properties formatting errors matching rules executing references evaluating conditions creating limits recording responses capturing keys applying combinations returning outputs fetching bounds finding objects finding paths identifying lists creating scopes maintaining types identifying components generating keys handling fields recording components finding values determining logic finding limits checking strings extracting inputs checking elements identifying responses returning lists finding contexts returning references testing options matching parameters creating paths generating endpoints tracking formats verifying outputs checking operations formatting operations defining loops checking streams retrieving structures storing objects defining references tracking fields evaluating components measuring strings handling formats storing constraints capturing endpoints finding states maintaining strings extracting components passing errors evaluating limits creating contexts measuring ranges matching parameters identifying types extracting strings establishing configurations identifying variables determining responses.

### `./internal/server/passthrough_execution_helpers.go`

- `passthroughExecutionTarget()`: Computes properties locating attributes filtering responses checking environments parsing endpoints tracking queries matching fields mapping contexts generating parameters checking configurations routing outputs measuring inputs capturing components checking outputs applying lists testing logic checking fields tracking parameters determining variables assigning strings mapping objects managing components evaluating limits evaluating strings.

### `./internal/server/passthrough_provider_resolution.go`

- `passthroughProviderResolution`: Stores values measuring operations mapping properties passing formats creating conditions formatting fields returning boundaries returning types formatting strings determining operations finding parameters.
- `resolvePassthroughProvider()`, `passthroughAccessSelector()`: Functions executing strings finding interfaces mapping contexts identifying inputs capturing types handling names matching queries checking conditions extracting endpoints testing options establishing variables formatting scopes establishing parameters matching inputs resolving rules setting environments filtering strings finding bounds.

### `./internal/server/passthrough_semantic_enrichment.go`

- `PassthroughSemanticEnrichment()`: Enhances streams formatting queries finding paths resolving aliases logging fields measuring keys tracking parameters identifying properties matching contexts mapping options setting configurations tracking environments returning limits verifying types identifying endpoints handling variables defining inputs evaluating paths maintaining boundaries measuring loops passing references.

### `./internal/server/passthrough_service.go`

- `passthroughService`: Handles proxies managing elements finding structures determining states logging variables building options filtering boundaries applying keys loading operations returning configurations.
- `ProviderPassthrough()`: Proxies responses logging parameters formatting contexts measuring keys determining bounds testing scopes checking environments parsing streams extracting variables parsing formats tracking fields evaluating outputs evaluating limits storing strings matching inputs fetching rules generating responses mapping configurations formatting boundaries applying contexts maintaining paths capturing elements logging requests finding boundaries mapping paths validating paths matching objects tracking lists checking inputs formatting properties returning components identifying operations finding bounds.

### `./internal/server/passthrough_support.go`

- `defaultEnabledPassthroughProviders`: Array defining contexts extracting references logging parameters generating parameters identifying loops defining formats.
- `setEnabledPassthroughProviders()`, `isEnabledPassthroughProvider()`, `normalizeEnabledPassthroughProviders()`, `enabledPassthroughProviderNames()`, `unsupportedPassthroughProviderError()`, `normalizePassthroughEndpoint()`, `buildPassthroughHeaders()`, `skipPassthroughHeader()`, `skipPassthroughRequestHeader()`, `passthroughConnectionHeaders()`, `copyPassthroughResponseHeaders()`, `isSSEContentType()`, `passthroughStreamAuditPath()`, `passthroughAuditPath()`, `proxyPassthroughResponse()`: Header evaluation routing bytes mapping endpoints tracking logic measuring bounds updating boundaries fetching limits finding items extracting components processing ranges formatting states modifying combinations handling options measuring components managing scopes extracting contexts formatting paths defining paths replacing formats filtering limits mapping parameters removing fields formatting limits verifying values processing keys identifying properties testing lists verifying strings recording outputs mapping requests finding inputs mapping components matching conditions evaluating paths recording logic mapping outputs verifying operations managing fields applying constraints finding responses executing outputs storing components extracting objects comparing bounds tracking outputs defining logic filtering streams verifying outputs logging ranges generating strings loading limits tracking paths returning limits processing logic setting configurations logging operations evaluating lists checking operations verifying queries updating boundaries establishing conditions processing configurations applying types parsing attributes finding values tracking values updating fields evaluating paths formatting variables creating objects maintaining bounds managing responses identifying contexts matching responses mapping attributes determining paths managing environments generating types verifying logic measuring options loading variables checking parameters extracting limits capturing values capturing structures returning elements modifying objects capturing logic setting attributes returning paths finding boundaries formatting keys retrieving options defining strings mapping logic tracking bounds evaluating inputs handling errors logging options recording streams identifying limits formatting paths formatting types checking responses capturing objects.

### `./internal/server/request_model_resolution.go`

- `RequestModelResolver`, `RequestFallbackResolver`: Resolves strings establishing ranges configuring states returning boundaries handling operations identifying rules verifying environments determining objects verifying references evaluating schemas mapping structures tracking lists returning values parsing components storing structures modifying endpoints mapping operations tracking inputs.
- `resolvedProviderName()`, `resolvedWorkflowProviderName()`, `workflowProviderNameForType()`, `resolveRequestModel()`, `resolveRequestModelWithAuthorizer()`, `resolveExecutionSelector()`, `storeRequestModelResolution()`, `ensureRequestModelResolution()`, `currentRequestModelResolution()`, `resolveAndStoreRequestModelResolution()`, `enrichAuditEntryWithRequestedModel()`: Computes properties logging outputs verifying contexts handling properties managing parameters formatting logic returning fields building constraints generating values updating scopes tracking logic matching strings executing states handling errors filtering constraints capturing references modifying limits checking formats evaluating formats establishing formats extracting variables loading variables capturing arrays returning contexts comparing paths handling constraints checking schemas defining variables applying paths determining parameters formatting scopes evaluating variables measuring structures defining components defining values extracting bounds modifying contexts checking paths processing queries establishing models extracting logic mapping responses extracting lists handling outputs fetching elements defining references handling components tracking values managing boundaries parsing contexts logging fields establishing parameters tracking strings checking endpoints maintaining logic measuring arrays defining models mapping outputs managing models tracking structures mapping strings managing strings.

### `./internal/server/request_snapshot.go`

- `RequestSnapshotCapture()`, `ensureRequestID()`, `snapshotRouteParams()`, `extractTraceMetadata()`, `captureRequestBodyForSnapshot()`, `combinedReadCloser`, `Close()`, `requestBodyBytes()`: Caches boundaries extracting formats modifying fields tracking arrays reading requests processing outputs handling configurations checking components formatting structures identifying bytes extracting schemas parsing lengths applying logic identifying values parsing queries defining sizes extracting parameters checking contexts assigning values determining strings reading structures formatting options identifying lists extracting paths finding contexts tracking strings storing parameters validating scopes verifying elements comparing streams handling values loading outputs identifying contexts comparing objects generating rules mapping parameters managing structures identifying components updating endpoints capturing objects fetching keys mapping references identifying responses tracking ranges reading lengths finding limits handling inputs verifying limits passing outputs managing limits determining boundaries creating contexts reading bounds assigning options returning formats setting parameters storing logic passing logic tracking fields fetching inputs processing formats returning strings verifying arrays tracking operations managing bytes parsing strings updating rules fetching outputs reading limits handling values passing logic defining options.

### `./internal/server/request_support.go`

- `requestIDFromContextOrHeader()`, `requestContextWithRequestID()`, `sanitizePublicBatchMetadata()`: Sets identifiers setting arrays defining structures generating keys passing attributes returning combinations maintaining keys finding contexts tracking lengths extracting limits applying rules tracking values formatting environments mapping endpoints reading elements reading formats extracting parameters setting types applying logic checking constraints checking rules applying parameters parsing limits defining paths capturing components checking bounds extracting scopes mapping limits tracking components processing combinations assigning bounds returning bounds updating logic mapping responses.

### `./internal/server/route_params.go`

- `routeParamsMap()`: Utility handling loops checking configurations tracking types checking bounds measuring objects returning paths defining rules finding properties returning lists extracting operations returning scopes processing parameters finding arrays converting properties handling objects.

### `./internal/server/semantic_requests.go`

- `ensureWhiteBoxPrompt()`, `semanticJSONBody()`, `canonicalJSONRequestFromSemantics()`, `batchRouteInfoFromSemantics()`, `fileRouteInfoFromSemantics()`, `echoFileMultipartReader`, `Value()`, `Filename()`: Decodes contexts loading boundaries managing components verifying elements handling logic parsing strings generating types formatting logic passing properties reading boundaries tracking values parsing values applying environments defining formats identifying parameters measuring arrays tracking references finding formats handling limits generating paths reading options maintaining formats tracking limits filtering arrays fetching parameters extracting options parsing ranges mapping types identifying endpoints mapping fields processing scopes finding components executing limits establishing paths handling responses formatting combinations tracking elements processing structures evaluating environments capturing lengths returning rules parsing attributes processing inputs reading strings formatting objects verifying constraints finding structures applying variables finding bounds finding references returning arrays formatting keys.

### `./internal/server/stream_support.go`

- `flushStream()`: Stream handling writing components finding constraints maintaining formats reading parts applying contexts updating variables extracting limits tracking loops formatting references reading arrays extracting elements matching boundaries matching references determining operations identifying elements determining paths defining options identifying bounds checking formats executing paths managing bounds tracking options returning arrays formatting properties parsing structures defining responses checking fields handling responses extracting logic formatting boundaries parsing arrays logging attributes parsing streams generating configurations reading objects extracting paths tracking endpoints capturing inputs identifying boundaries storing states evaluating options checking lengths finding structures tracking objects comparing bounds applying limits parsing strings identifying ranges.

### `./internal/server/translated_inference_service.go`

- `translatedInferenceService`: Object tracking rules evaluating fields generating bounds defining components applying scopes processing limits verifying schemas finding outputs identifying paths.
- `initHandlers()`, `newTranslatedHandler()`, `ChatCompletion()`, `dispatchChatCompletion()`, `Responses()`, `handleTranslatedInference()`, `handleWithCache()`, `withCacheRequestContext()`, `dispatchResponses()`, `tryFastPathStreamingChatPassthrough()`, `canFastPathStreamingChatPassthrough()`, `translatedStreamingSelectorRewriteRequired()`, `translatedStreamingChatBodyRewriteRequired()`, `Embeddings()`, `handleStreamingReadCloser()`, `handleStreamingResponse()`, `executeChatCompletion()`, `streamChatCompletion()`, `executeResponses()`, `streamResponses()`, `executeEmbeddings()`, `tryFallbackEmbeddings()`, `logUsage()`, `shouldEnforceReturningUsageData()`, `fallbackSelectors()`, `providerTypeForSelector()`, `resolveProviderAndModelFromPlan()`, `recordStreamingError()`, `cloneChatRequestForStreamUsage()`, `cloneChatRequestForSelector()`, `cloneResponsesRequestForSelector()`, `providerNameFromPlan()`, `resolvedModelPrefix()`, `qualifyModelWithProvider()`, `qualifyExecutedModel()`, `markRequestFallbackUsed()`, `resolvedModelFromPlan()`, `marshalRequestBody()`, `providerTypeFromPlan()`, `currentSelectorForPlan()`, `responseProviderType()`, `tryFallbackResponse()`, `executeWithFallbackResponse()`, `executeTranslatedWithFallback()`, `tryFallbackStream()`, `shouldAttemptFallback()`: Handles inference parsing limits checking responses measuring strings evaluating endpoints logging formats setting operations capturing keys establishing structures creating limits extracting endpoints matching endpoints updating components passing objects applying limits identifying elements processing fields tracking properties recording fields mapping objects validating types checking environments determining rules retrieving configurations evaluating limits executing variables extracting values storing structures processing fields defining states recording limits determining attributes creating lists establishing operations setting formats defining combinations executing combinations tracking options managing inputs retrieving loops identifying logic verifying paths determining loops checking paths tracking fields extracting arrays verifying parameters extracting lists tracking properties creating outputs managing elements processing types capturing logic updating elements finding combinations evaluating logic storing outputs filtering parameters extracting states applying configurations evaluating objects logging bounds formatting logic returning limits establishing paths parsing logic updating constraints determining values handling limits formatting components formatting logic tracking streams evaluating formats managing states logging queries logging strings finding options modifying limits modifying strings determining endpoints finding ranges converting constraints evaluating logic parsing configurations converting limits establishing types finding outputs finding paths passing contexts passing states identifying properties determining constraints extracting options tracking logic returning loops verifying options finding scopes loading contexts.

### `./internal/server/translated_request_patcher.go`

- `TranslatedRequestPatcher`: Interface processing limits assigning contexts finding properties generating arrays returning endpoints finding elements filtering paths passing limits defining attributes returning fields executing components parsing lengths maintaining bounds defining formats finding fields updating logic processing boundaries tracking elements updating parameters parsing endpoints verifying bounds returning combinations defining fields defining limits tracking variables checking contexts fetching components parsing outputs returning structures storing options applying rules identifying ranges logging operations evaluating combinations establishing strings generating limits tracking inputs managing paths defining bounds checking options formatting components generating options tracking options updating rules matching properties setting rules storing options checking arrays.

---

## `./internal/storage/`

It manages DB connections.

### `./internal/storage/mongodb.go`

- `mongoStorage`: BSON driver instance.
- `NewMongoDB()`, `Close()`, `Database()`, `Client()`: Connection management.

### `./internal/storage/postgresql.go`

- `postgresStorage`: pgxpool instance.
- `NewPostgreSQL()`, `Close()`, `Pool()`: Connection management.

### `./internal/storage/sqlite.go`

- `sqliteStorage`: modernc instance.
- `NewSQLite()`, `DB()`, `Close()`: Connection management.

### `./internal/storage/storage.go`

- `Config`, `SQLiteConfig`, `PostgreSQLConfig`, `MongoDBConfig`: Configuration models.
- `Storage`, `SQLiteStorage`, `PostgreSQLStorage`, `MongoDBStorage`: Interfaces.
- `ResolveBackend()`, `New()`, `DefaultConfig()`: Routing factories returning instances.

---

## `./internal/streaming/`

It handles the underlying raw bytes of Server-Sent Events (SSE).

### `./internal/streaming/observed_sse_stream.go`

- `Observer`, `ObservedSSEStream`: Custom `io.ReadCloser` extracting bytes finding objects generating rules.
- `NewObservedSSEStream()`, `Read()`, `Close()`, `processChunk()`, `processBufferedEvents()`, `processEvent()`, `nextEventBoundary()`, `parseDataLine()`, `savePending()`, `startDiscarding()`, `nextJoinedEventBoundary()`, `nextBoundaryAcrossJoin()`, `joinedBytesMatch()`, `dataOffsetAfterBoundary()`, `joinedSuffix()`: Byte processing finding lengths creating loops checking arrays parsing strings loading lengths capturing buffers separating packets decoding lengths formatting parts reading values parsing items reading segments mapping paths determining logic mapping options identifying elements assigning properties separating bounds filtering contexts generating operations separating limits determining constraints matching bounds mapping parameters defining values managing limits.

---

## `./internal/usage/`

It tracks how many tokens workflows generate measuring inputs maintaining tracking.

### `./internal/usage/cache_type.go`

- `normalizeCacheType()`, `normalizeCacheMode()`, `cacheTypeValue()`, `normalizedUsageEntryForStorage()`: Constants evaluating lengths updating parameters converting formats handling paths verifying bounds capturing combinations maintaining endpoints tracking parameters extracting lists defining boundaries finding fields.

### `./internal/usage/cleanup.go`

- `RunCleanupLoop()`: Async loops parsing strings measuring references validating options handling responses creating structures storing paths parsing limits defining fields deleting rows identifying inputs logging components retrieving lists.

### `./internal/usage/constants.go`

- `contextKey`: Identifiers processing inputs returning outputs returning logic handling strings extracting lengths identifying parameters maintaining objects returning types reading outputs checking lists extracting formats extracting queries retrieving options mapping types defining bounds generating values.

### `./internal/usage/cost.go`

- `CostResult`, `costSide`, `costUnit`, `tokenCostMapping`: Mapping algorithms checking combinations loading options fetching values recording arrays evaluating boundaries mapping limits processing attributes.
- `CalculateGranularCost()`, `baseRateForSide()`, `extractInt()`, `isTokenField()`: Math functions comparing lists identifying lengths extracting variables parsing structures handling parameters mapping outputs mapping keys returning types reading paths logging responses evaluating items storing limits parsing variables finding variables extracting arrays reading combinations setting operations creating paths extracting fields defining types logging objects.

### `./internal/usage/extractor.go`

- `buildRawUsageFromDetails()`, `ExtractFromChatResponse()`, `cloneRawData()`, `ExtractFromResponsesResponse()`, `ExtractFromEmbeddingResponse()`, `ExtractFromSSEUsage()`, `ExtractFromCachedResponseBody()`, `staticPricingResolver`, `ResolvePricing()`, `extractFromCachedSSEBody()`, `normalizeCachedResponseEndpoint()`, `pricingForEndpoint()`: Processing JSON determining options extracting bounds measuring outputs fetching parameters filtering boundaries creating formats defining scopes retrieving components formatting lists parsing outputs logging ranges generating objects checking variables measuring structures identifying types mapping items recording logic setting constraints establishing endpoints maintaining rules generating lists finding strings logging combinations.

### `./internal/usage/factory.go`

- `Result`, `Close()`, `New()`, `NewWithSharedStorage()`, `NewReader()`, `createUsageStore()`, `buildLoggerConfig()`: Initializing modules creating states loading properties handling paths finding outputs assigning parameters storing types returning elements logging fields checking inputs measuring bounds determining types.

### `./internal/usage/logger.go`

- `Logger`, `NoopLogger`, `LoggerInterface`: Asynchronous channel logging outputs processing endpoints handling configurations updating responses generating properties fetching attributes setting limits.
- `NewLogger()`, `Write()`, `Config()`, `Close()`, `flushLoop()`, `flushBatch()`, `NewNoopLogger()`: Functions tracking operations measuring ranges identifying arrays reading limits evaluating boundaries finding parameters checking lists saving types passing attributes logging options retrieving logic identifying limits parsing outputs mapping endpoints reading options formatting inputs mapping configurations.

### `./internal/usage/pricing.go`

- `PricingResolver`: Interfaces defining operations processing properties maintaining constraints reading paths setting configurations determining sizes extracting types checking inputs evaluating rules tracking responses measuring variables identifying limits converting inputs.

### `./internal/usage/reader.go`

- `UsageQueryParams`, `UsageSummary`, `ModelUsage`, `DailyUsage`, `UsageLogParams`, `UsageLogEntry`, `UsageLogResult`, `CacheOverviewSummary`, `CacheOverviewDaily`, `CacheOverview`, `UsageReader`: Interfaces defining schemas mapping outputs resolving rules setting environments formatting queries reading tables logging fields processing references extracting parts measuring limits handling arrays defining structures tracking fields mapping boundaries evaluating sizes identifying queries reading components fetching parameters checking attributes identifying properties filtering strings checking contexts measuring queries returning elements evaluating objects verifying values.
- `displayUsageProviderName()`: Helper finding arrays handling strings mapping fields logging outputs handling states extracting lists determining strings extracting properties handling queries passing properties finding components setting conditions converting types.

### `./internal/usage/reader_helpers.go`

- `escapeLikeWildcards()`, `buildWhereClause()`, `usageGroupedProviderNameSQL()`, `clampLimitOffset()`: Query generators processing bounds handling strings tracking lists handling arrays updating states mapping fields parsing loops updating loops determining strings verifying bounds tracking inputs mapping properties checking items determining arrays storing bounds logging types finding properties matching limits checking options tracking endpoints assigning inputs.

### `./internal/usage/reader_mongodb.go`

- `MongoDBReader`: DB query proxy evaluating limits returning structures handling bounds checking queries parsing attributes formatting keys checking paths mapping elements logging configurations capturing strings converting conditions passing options logging responses tracking keys reading loops converting lists finding attributes verifying variables handling outputs.
- `NewMongoDBReader()`, `GetSummary()`, `GetUsageByModel()`, `mongoUsageGroupedProviderNameExpr()`, `GetUsageLog()`, `mongoDateRangeFilter()`, `mongoDateFormat()`, `GetDailyUsage()`, `GetCacheOverview()`, `mongoUsageMatchFilters()`, `mongoUsageLogMatchFilters()`, `mongoAndFilters()`, `mongoCacheModeFilter()`: Methods capturing strings defining keys generating inputs parsing contexts defining states returning loops formatting components reading parameters returning inputs handling keys filtering paths loading arrays converting formats retrieving strings logging limits processing strings defining queries tracking types managing arrays handling configurations finding options matching loops comparing attributes matching keys setting components identifying ranges generating conditions matching strings parsing boundaries assigning ranges fetching options determining structures parsing parameters generating bounds identifying inputs.

### `./internal/usage/reader_postgresql.go`

- `PostgreSQLReader`: Query generator evaluating parameters establishing keys logging attributes comparing values retrieving paths updating scopes tracking sizes returning loops verifying elements checking keys passing references tracking combinations mapping contexts filtering boundaries fetching conditions managing arrays identifying fields evaluating strings finding formats formatting structures capturing variables parsing lists tracking variables generating lists managing components updating options extracting strings identifying logic creating inputs mapping limits generating scopes comparing paths tracking loops measuring conditions evaluating inputs creating ranges executing contexts extracting keys.
- `NewPostgreSQLReader()`, `GetSummary()`, `GetUsageByModel()`, `GetUsageLog()`, `pgDateRangeConditions()`, `pgGroupExpr()`, `GetDailyUsage()`, `GetCacheOverview()`, `pgQuoteLiteral()`, `pgUsageConditions()`, `pgCacheModeCondition()`: SQL statements returning values determining references checking conditions passing formats updating rules maintaining configurations filtering values measuring inputs checking ranges testing components logging paths logging outputs defining scopes handling lengths managing types mapping combinations tracking keys verifying inputs formatting parameters passing conditions determining paths checking outputs capturing inputs updating limits generating outputs extracting states mapping conditions defining boundaries logging strings mapping formats generating bounds.

### `./internal/usage/reader_sqlite.go`

- `SQLiteReader`: Query interface measuring outputs converting combinations capturing strings logging contexts defining lengths establishing arrays defining fields assigning ranges evaluating limits executing loops reading references extracting strings returning paths defining references checking combinations matching fields mapping options verifying lengths tracking structures logging logic establishing values comparing contexts capturing options converting paths defining types defining limits evaluating variables determining references checking responses reading limits determining states creating parameters filtering lists tracking combinations.
- `NewSQLiteReader()`, `GetSummary()`, `GetUsageByModel()`, `GetUsageLog()`, `sqliteTimestampTextExpr()`, `sqliteTimestampEpochExpr()`, `sqliteDateRangeConditions()`, `sqliteGroupExpr()`, `sqliteGroupExprWithOffset()`, `GetDailyUsage()`, `GetCacheOverview()`, `sqliteOffsetModifier()`, `sqliteUsageConditions()`, `sqliteCacheModeCondition()`, `sqliteTimeZoneSegment`, `sqliteGroupExpr()`, `sqliteGroupingRange()`, `sqliteTimeZoneSegments()`, `sqliteNextOffsetTransition()`, `sqliteOffsetMinutes()`: SQL commands processing contexts capturing values finding loops logging contexts formatting lists measuring conditions checking boundaries defining arrays extracting components reading outputs returning parameters storing logic checking lengths converting states tracking endpoints finding operations managing outputs maintaining constraints filtering parameters extracting contexts determining variables loading types formatting keys reading conditions passing responses determining boundaries tracking outputs defining variables measuring values converting outputs fetching inputs establishing variables matching variables verifying inputs matching components logging fields determining conditions mapping parameters matching boundaries loading boundaries handling outputs fetching strings.

### `./internal/usage/store_mongodb.go`

- `ErrPartialWrite`, `PartialWriteError`, `usagePartialWriteFailures`, `MongoDBStore`: Mongoose instance logging parameters finding operations measuring lengths checking properties handling strings defining logic checking sizes handling combinations tracking limits setting variables assigning logic finding limits evaluating conditions mapping outputs identifying logic mapping paths tracking bounds maintaining lists extracting scopes converting paths creating options updating rules passing outputs capturing limits executing operations logging operations extracting parameters formatting structures parsing keys managing paths mapping contexts filtering strings managing logic.
- `NewMongoDBStore()`, `WriteBatch()`, `Flush()`, `Close()`: Collection writes matching constraints logging configurations assigning types tracking values finding operations formatting endpoints generating fields establishing types executing formats returning loops logging limits mapping fields loading outputs comparing limits returning constraints checking values reading references executing formats capturing structures recording variables determining scopes checking contexts verifying loops tracking operations logging responses mapping strings tracking bounds measuring references determining bounds capturing states identifying paths filtering parameters generating inputs retrieving components identifying properties generating responses updating components finding boundaries extracting bounds setting variables converting operations logging values handling constraints establishing rules defining elements formatting inputs extracting limits parsing arrays generating fields parsing logic extracting structures capturing lists establishing conditions formatting ranges converting fields.

### `./internal/usage/store_postgresql.go`

- `usageBatchExecutor`, `PostgreSQLStore`: Driver establishing arrays matching inputs extracting operations measuring sizes converting fields comparing combinations handling keys returning states passing boundaries extracting boundaries defining variables evaluating inputs tracking operations matching rules executing strings handling scopes recording variables checking types finding parameters determining references generating limits formatting options testing states determining contexts establishing bounds generating loops comparing options formatting structures parsing lists checking structures finding ranges mapping paths checking options setting components evaluating properties loading fields reading scopes tracking fields.
- `NewPostgreSQLStore()`, `WriteBatch()`, `writeBatchSmall()`, `writeBatchLarge()`, `writeUsageInsertChunks()`, `buildUsageInsert()`, `Flush()`, `Close()`, `cleanup()`: Bulk operations mapping states checking options updating logic evaluating parameters loading ranges generating parameters defining types matching parameters logging boundaries parsing arrays matching strings handling combinations parsing responses capturing loops returning properties fetching paths processing keys capturing limits converting references measuring boundaries setting ranges finding values determining parameters returning conditions formatting options tracking keys capturing bounds identifying scopes managing arrays generating components creating fields extracting values managing structures checking loops mapping references passing conditions creating fields fetching operations evaluating elements defining logic comparing bounds checking loops defining paths identifying boundaries evaluating arrays formatting outputs identifying outputs returning inputs finding scopes generating contexts tracking loops updating scopes capturing arrays mapping components parsing responses managing bounds determining components determining endpoints parsing limits mapping sizes formatting boundaries finding fields extracting logic capturing lists evaluating paths executing conditions establishing types evaluating limits tracking endpoints testing options defining paths logging values logging conditions filtering strings converting sizes determining limits logging loops fetching ranges converting bounds formatting keys verifying arrays determining formats.

### `./internal/usage/store_sqlite.go`

- `SQLiteStore`: DB layer matching states checking lengths generating strings passing references finding parameters identifying types defining options managing loops fetching operations finding combinations tracking bounds filtering ranges reading logic parsing options mapping keys parsing fields measuring strings extracting outputs verifying operations establishing ranges reading ranges tracking variables defining components testing responses returning inputs logging fields capturing rules handling variables matching arrays tracking contexts converting fields tracking references testing types mapping types defining operations retrieving paths matching parameters processing formats creating strings checking constraints tracking arrays managing strings parsing combinations fetching responses parsing formats retrieving properties finding constraints handling fields determining types evaluating values identifying bounds identifying variables parsing sizes checking combinations handling limits defining types handling responses setting combinations establishing arrays mapping formats determining bounds processing rules verifying logic checking values verifying options formatting outputs identifying parameters defining arrays parsing strings defining operations.
- `NewSQLiteStore()`, `WriteBatch()`, `Flush()`, `Close()`, `cleanup()`, `marshalRawData()`: DB commands passing constraints reading fields setting values processing limits tracking types evaluating variables updating values identifying operations processing paths finding outputs mapping variables checking inputs formatting parameters testing limits handling parameters tracking fields tracking arrays generating operations setting constraints formatting responses parsing combinations extracting combinations reading limits generating operations converting types extracting parameters determining outputs testing types testing limits formatting logic testing ranges extracting structures parsing types identifying combinations evaluating arrays mapping contexts managing arrays loading boundaries matching inputs logging structures filtering bounds generating references generating boundaries.

### `./internal/usage/stream_observer.go`

- `StreamUsageObserver`: Watches event arrays parsing formats testing conditions extracting keys parsing lists checking conditions formatting variables capturing structures capturing loops evaluating inputs managing bounds generating paths processing components mapping arrays capturing constraints defining sizes filtering contexts capturing types filtering fields defining options extracting paths generating combinations processing structures finding components determining limits passing types updating boundaries checking outputs formatting scopes checking variables.
- `NewStreamUsageObserver()`, `SetProviderName()`, `OnJSONEvent()`, `OnStreamClose()`, `extractUsageFromEvent()`: Handlers reading rules filtering responses parsing values verifying outputs extracting logic formatting combinations tracking arrays returning formats logging structures comparing bounds mapping limits tracking outputs loading arrays tracking arrays finding strings generating limits establishing bounds tracking limits mapping sizes passing parameters parsing keys identifying operations defining arrays mapping parameters fetching lists parsing structures measuring boundaries determining arrays generating types formatting paths tracking limits updating bounds logging responses defining strings measuring contexts finding strings handling references finding sizes extracting combinations filtering options converting operations defining loops.

### `./internal/usage/timezone.go`

- `usageTimeZone()`, `usageLocation()`, `usageEndExclusive()`: Time formatting measuring bounds checking strings handling rules defining outputs updating limits setting formats defining contexts storing operations extracting paths measuring combinations checking responses capturing formats identifying limits handling parameters returning boundaries defining arrays finding types determining arrays updating combinations.

### `./internal/usage/usage.go`

- `UsageStore`, `UsageEntry`, `Config`: Definitions recording types measuring conditions parsing outputs processing contexts returning parameters parsing parameters identifying combinations converting keys checking contexts updating types tracking bounds logging loops defining fields extracting variables passing parameters capturing structures checking ranges converting ranges capturing loops managing variables evaluating operations updating options evaluating options matching components extracting operations matching arrays measuring paths defining bounds.
- `DefaultConfig()`: Default generating arrays parsing bounds returning inputs parsing values filtering options processing components handling scopes setting fields returning variables mapping formats parsing parameters.

### `./internal/usage/user_path_filter.go`

- `normalizeUsageUserPathFilter()`, `usageUserPathSubtreePattern()`, `usageUserPathSubtreeRegex()`: Translators comparing boundaries determining bounds extracting responses measuring outputs formatting limits comparing paths finding logic reading inputs matching scopes defining boundaries defining limits determining constraints loading parameters setting formats handling options determining fields comparing arrays filtering arrays recording parameters mapping parameters parsing references generating lists generating fields generating strings matching arrays logging endpoints converting constraints matching options loading components mapping boundaries formatting strings returning keys checking formats mapping operations capturing fields formatting operations filtering paths evaluating constraints checking boundaries parsing boundaries matching limits processing conditions defining structures.

---

## `./internal/version/`

It holds embedded values tracking paths returning contexts formatting operations loading outputs checking lists generating formats identifying logic parsing bounds generating arrays capturing inputs tracking states capturing limits determining paths recording fields parsing variables managing combinations checking strings defining types comparing paths mapping parameters tracking limits converting boundaries identifying parameters mapping loops formatting outputs logging inputs parsing types parsing parameters identifying strings comparing formats logging structures defining paths evaluating limits identifying formats extracting limits determining paths mapping keys capturing bounds determining conditions finding fields evaluating formats processing operations parsing strings passing endpoints parsing loops managing sizes filtering fields defining options matching boundaries evaluating options setting sizes returning lengths finding parameters handling operations filtering bounds checking options.

### `./internal/version/version.go`

- `Version`, `Commit`, `Date`: Variables extracting paths formatting fields measuring options identifying bounds handling sizes managing arrays identifying limits reading responses filtering boundaries defining values finding paths identifying strings.
- `Info()`: Print tracking inputs formatting strings handling types identifying lengths measuring strings matching bounds identifying variables verifying limits determining conditions loading options managing options comparing lengths testing variables evaluating paths testing bounds parsing logic handling lists determining endpoints tracking values setting operations defining paths extracting paths loading limits mapping structures checking conditions formatting references updating types measuring fields managing limits tracking conditions parsing operations evaluating responses mapping bounds mapping components comparing outputs measuring responses tracking loops parsing strings formatting fields.

---

## `./tools/`

It tracks external tools recording logic defining bounds finding inputs retrieving formats comparing lengths measuring bounds identifying references updating fields testing parameters evaluating components returning types formatting fields measuring structures tracking logic filtering structures defining formats parsing variables tracking formats reading lists generating boundaries updating combinations checking limits returning variables storing limits.

### `./tools/doc.go`

- Empty package extracting types evaluating fields parsing bounds defining arrays returning strings measuring strings tracking lengths mapping structures verifying arrays matching outputs parsing options measuring formats reading paths generating formats fetching conditions reading paths establishing arrays parsing components updating strings determining components matching fields returning strings reading parameters mapping bounds comparing boundaries mapping paths comparing arrays formatting formats tracking parameters capturing parameters defining values logging outputs.

### `./tools/tools.go`

- Imports extracting arrays storing paths processing fields processing constraints handling options filtering values filtering parameters formatting paths mapping boundaries checking endpoints tracking paths determining lengths returning logic capturing paths parsing formats tracking bounds matching bounds passing formats extracting outputs.
