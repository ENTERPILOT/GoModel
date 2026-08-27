# Contributing to GoModel

Thank you for contributing!

Please note that this is currently a one-person venture, so reviews and replies may take some time. Sorry for any delays, and thank you for your patience.

## Guidelines

We use AI tools to speed up development and code review. Because of that, you may see many comments from tools such as Greptile, CodeRabbit, or similar reviewers.

You do not need to fix every AI-generated comment. Sometimes these tools miss the project context or are not aligned with the project vision. Please use your judgment, but try to review at least the comments marked as high priority - P1, critical, major etc.

You may also find helpful this short note about our technical philosophy:

https://gomodel.enterpilot.io/docs/about/technical-philosophy

## Commit Messages

Please use Conventional Commits for commit subjects and PR titles:

`type(scope): short summary`

Allowed types are `feat`, `fix`, `perf`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, and `revert`.

## Dashboard frontend

The admin dashboard is a Svelte 5 single-page app in `web/dashboard/`. The
built assets are generated under `internal/admin/dashboard/static/dist/` and
embedded into the Go binary. Generated assets are intentionally ignored by
Git; use the Make targets rather than invoking `go build` directly so the
dashboard is built first.

Dashboard translations are welcome; see the
[translation guide](web/dashboard/src/lib/i18n/README.md).

When you change dashboard sources:

```sh
make frontend        # install locked dependencies as needed, then run Vite
make test-dashboard  # frontend unit tests
make check-dashboard # Svelte and TypeScript checks
make build            # build the dashboard and the Go binary
```

Do not commit `static/dist`; CI, Docker, and release builds generate it from
the committed sources and lockfile. `make run` starts both the gateway and the
watched Vite frontend. Open `http://localhost:5173/admin/dashboard`; Vite keeps
the browser on one origin while proxying `/admin`, `/v1`, and `/version` to the
gateway on :8080. Use `make run-backend` when you specifically want the
embedded production dashboard on `http://localhost:8080/admin/dashboard`.

## Questions

For questions, ideas, or general discussion, please use GitHub Discussions:

https://github.com/ENTERPILOT/GoModel/discussions

You can also reach out on Discord. If something is urgent, feel free to ping me: `SantiagoDePL`.

## License

The project is currently licensed under the MIT License.

If you want to understand our perspective on the future of the license, please read:

https://gomodel.enterpilot.io/docs/about/license

By submitting a contribution, you confirm that you have the right to submit it.

You also grant the project maintainers permission to use, sublicense, and relicense your contribution as part of the project under the current or future project licenses.
