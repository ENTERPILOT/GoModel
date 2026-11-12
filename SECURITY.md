# Security Policy

## Supported Versions

Security fixes ship in the
[latest release](https://github.com/ENTERPILOT/GoModel/releases/latest) only.
Older releases are not patched, so please confirm an issue against the latest
release before reporting it.

## Reporting a Vulnerability

Please report security issues privately through GitHub:
[Report a vulnerability](https://github.com/ENTERPILOT/GoModel/security/advisories/new).
This opens a private advisory only the maintainers can see, and lets us develop
and review a fix in a private fork before anything is disclosed.

Alternatively, you can email `abuse (#) enterpilot.io`, or reach
SantiagoDePL#0353 via GoModel's Discord server.

Please do not open a public issue for a security report.

## What to Expect

- An acknowledgement within 3 business days.
- A status update within 7 days of the report: whether we could reproduce it,
  how severe we consider it, and what happens next.
- Credit in the advisory, unless you prefer to stay anonymous.

## Disclosure

We follow coordinated disclosure. Please keep a report private until a fixed
release is published or 90 days have passed since the report, whichever comes
first. If a fix needs longer than that, we will say so and agree a date with
you.

When a fix is released we:

- publish a GitHub Security Advisory naming the affected and patched versions,
- request a CVE for it,
- retract the affected versions in `go.mod` when the issue affects people who
  build GoModel as a Go module.

## Scope

These are documented behaviors, not vulnerabilities:

- **No master key.** With `GOMODEL_MASTER_KEY` empty and no authentication
  extension, `/admin/*` is open to anyone who can reach the port, and so is the
  API until a managed key exists. GoModel warns about this at startup; see the
  [production guide](https://gomodel.enterpilot.io/docs/guides/production).
  Still in scope: an authentication bypass once a master key is set or disabled
  with `MASTER_KEY_DISABLED=true`, and anything that lets a party who cannot
  reach the gateway directly act on it.
- **Plugins.** A loaded `.so` plugin is trusted code running in the gateway
  process; loading one is equivalent to changing the binary. What a plugin can
  do once the operator loads it is out of scope.
- **Opt-in diagnostic endpoints.** `/metrics` and `pprof` are off by default
  and unauthenticated when enabled.
