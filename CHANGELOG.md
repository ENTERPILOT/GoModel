# Changelog

## Unreleased

### Migration notes

- Budget management depends on usage tracking. If `USAGE_ENABLED=false`, GoModel starts with budgets disabled and logs a warning even when `BUDGETS_ENABLED=true`; enable both `USAGE_ENABLED` and `BUDGETS_ENABLED` to enforce budgets.
