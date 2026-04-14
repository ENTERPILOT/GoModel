# Prometheus Metrics Configuration Guide

This guide explains how to configure Prometheus metrics in GoModel.

## Quick Start

### Disabled by Default

Metrics are **disabled by default**. To enable metrics collection, set `METRICS_ENABLED=true` and start GoModel:

```bash
export METRICS_ENABLED=true
./bin/gomodel
# Metrics available at http://localhost:8080/metrics
```

### Disable Metrics

**Option 1: Environment Variable**

```bash
export METRICS_ENABLED=false
./bin/gomodel
```

**Option 2: .env file**

```bash
echo "METRICS_ENABLED=false" >> .env
./bin/gomodel
```

**Option 3: config.yaml**

```yaml
metrics:
  enabled: false
```

### Custom Metrics Endpoint

Change the default `/metrics` path:

```bash
export METRICS_ENDPOINT=/internal/prometheus
./bin/gomodel
```

## Configuration Options

### Via Environment Variables

| Variable           | Default    | Description                       |
| ------------------ | ---------- | --------------------------------- |
| `METRICS_ENABLED`  | `false`    | Enable/disable metrics collection |
| `METRICS_ENDPOINT` | `/metrics` | HTTP path for metrics endpoint    |

### Via config.yaml

```yaml
metrics:
  # Enable or disable Prometheus metrics collection
  # When disabled, no metrics are collected and endpoint returns 404
  enabled: true

  # HTTP endpoint path where metrics are exposed
  endpoint: "/metrics"
```

## Examples

### Production Setup (Metrics Enabled)

**.env**

```bash
PORT=8080
GOMODEL_MASTER_KEY=your-secret-key
METRICS_ENABLED=true
METRICS_ENDPOINT=/metrics
OPENAI_API_KEY=sk-...
```

### Development Setup (Metrics Disabled)

**.env**

```bash
PORT=8080
METRICS_ENABLED=false
OPENAI_API_KEY=sk-...
```

### Custom Endpoint for Internal Monitoring

**config.yaml**

```yaml
server:
  port: "8080"
  master_key: "${GOMODEL_MASTER_KEY}"

metrics:
  enabled: true
  endpoint: "/internal/prometheus" # Custom path

providers:
  openai:
    type: "openai"
    api_key: "${OPENAI_API_KEY}"
```

## Verification

### Check if Metrics are Enabled

Start the server and look for log messages:

**Metrics Enabled:**

```json
{ "level": "INFO", "msg": "prometheus metrics enabled", "endpoint": "/metrics" }
```

**Metrics Disabled:**

```json
{ "level": "INFO", "msg": "prometheus metrics disabled" }
```

### Test Metrics Endpoint

**When Enabled:**

```bash
curl http://localhost:8080/metrics
# Returns Prometheus metrics in text format
```

**When Disabled:**

```bash
curl http://localhost:8080/metrics
# Returns 404 Not Found
```

## Performance Impact

### Metrics Enabled

- Minimal overhead: ~100ns per request for hook execution
- Memory: ~1MB for metric storage (depends on cardinality)
- CPU: Negligible impact (<0.1% in benchmarks)

### Metrics Disabled

- **Zero overhead**: No hooks registered, no collection
- Metrics library is still linked but inactive
- Recommended for maximum performance in non-production environments

## Security Considerations

### Exposing Metrics Endpoint

The `/metrics` endpoint is protected by the master key authentication when a master key is configured, just like other HTTP endpoints. If no master key is configured, the endpoint is accessible without authentication, which allows Prometheus to scrape metrics without credentials.

If you need to protect the metrics endpoint further:

1. **Use a custom internal path:**

   ```yaml
   metrics:
     endpoint: "/internal/prometheus" # Harder to guess
   ```

2. **Use network-level security:**
   - Configure firewall rules to allow only Prometheus server
   - Use private network for metrics collection
   - Deploy Prometheus in the same VPC/network

3. **Reverse proxy with authentication:**
   ```nginx
   location /metrics {
       auth_basic "Metrics";
       auth_basic_user_file /etc/nginx/.htpasswd;
       proxy_pass http://gomodel:8080/metrics;
   }
   ```

## Prometheus Configuration

### Scrape Config

**prometheus.yml**

```yaml
scrape_configs:
  - job_name: "gomodel"
    static_configs:
      - targets: ["localhost:8080"]
    metrics_path: "/metrics" # Or your custom path
    scrape_interval: 15s
    scrape_timeout: 10s
```

### With Custom Endpoint

```yaml
scrape_configs:
  - job_name: "gomodel"
    static_configs:
      - targets: ["localhost:8080"]
    metrics_path: "/internal/prometheus" # Custom path
    scrape_interval: 15s
```

## Troubleshooting

### Metrics Endpoint Returns 404

**Cause:** Metrics are disabled

**Solution:**

```bash
# Check configuration
echo $METRICS_ENABLED  # Should be "true" or empty (defaults to true)

# Enable metrics
export METRICS_ENABLED=true
./bin/gomodel
```

### No Metrics Data Appearing

**Cause:** No requests have been made yet

**Solution:** Make some requests to generate metrics:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-master-key" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Hi"}]}'

# Then check metrics
curl http://localhost:8080/metrics | grep gomodel_requests_total
```

### Custom Endpoint Not Working

**Cause:** Endpoint must start with `/`

**Incorrect:**

```bash
export METRICS_ENDPOINT=metrics  # Missing leading slash
```

**Correct:**

```bash
export METRICS_ENDPOINT=/metrics  # Has leading slash
```

## Best Practices

### Development

- **Disable metrics** for faster startup and reduced noise
- Enable only when testing observability features

### Staging

- **Enable metrics** to test monitoring setup
- Use custom endpoint if needed for security

### Production

- **Enable metrics** for full observability
- Set up Prometheus alerting
- Use Grafana dashboards for visualization
- Consider custom endpoint for security
- Monitor metric cardinality to avoid explosion

## Migration Guide

If you're upgrading from a version without configurable metrics:

### Before (Always Enabled)

```bash
# Metrics were always enabled at /metrics
./bin/gomodel
```

### After (Configurable, Default Enabled)

```bash
# No change needed - metrics still enabled by default
./bin/gomodel

# But now you can disable if needed
export METRICS_ENABLED=false
./bin/gomodel
```

## See Also

- [PROMETHEUS_IMPLEMENTATION.md](PROMETHEUS_IMPLEMENTATION.md) - Full implementation details
- [config/config.yaml](config/config.yaml) - Complete configuration
- [.env.template](.env.template) - Environment variable template
