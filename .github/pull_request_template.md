# Summary

Describe what changed and why.

## PR Title Format (required)

Use Conventional Commits in the PR title:

`type(scope): short summary`

Examples:
- `feat(cache): add Redis-backed model cache`
- `fix(streaming): flush done marker in SSE`
- `docs(config): clarify provider auto-discovery`

Allowed `type` values:
- `feat`
- `fix`
- `perf`
- `docs`
- `refactor`
- `test`
- `build`
- `ci`
- `chore`
- `revert`

Breaking changes:
- Add `!` before `:` (example: `feat(api)!: remove legacy endpoint`)

## Release Notes

- User-facing work should use `feat`, `fix`, `perf`, or `docs`.
- Internal-only work (`test`, `ci`, `build`, `chore`, many `refactor`s) is auto-labeled and excluded from release notes.
- Use `release:skip` label to explicitly exclude an item from release notes.
