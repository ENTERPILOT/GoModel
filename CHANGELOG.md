# Changelog

## Unreleased

### Migration notes

- Budget defaults are `USAGE_ENABLED=true` and `BUDGETS_ENABLED=true`.
- Minimal enablement:

  ```bash
  USAGE_ENABLED=true BUDGETS_ENABLED=true ./gomodel
  ```

- `internal/app/app.go` disables budgets when it sees `BUDGETS_ENABLED=true` with `USAGE_ENABLED=false`, because budget checks depend on usage data.
