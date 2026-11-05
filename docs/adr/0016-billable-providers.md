# ADR-0016: Billable Providers

## Status

Accepted

## Context

Operators run GoModel as a platform where a user path is one customer. Two
kinds of providers serve such a customer: the operator's own, whose cost the
customer owes, and the customer's own API key, registered as a provider
instance of its own (`openai-<customer>`) through the provider-credentials
API, whose cost the customer pays upstream directly.

Budgets and billing did not tell them apart. A user-path budget summed every
usage row under the path, so a customer's own OpenAI spend ate the budget
meant for the operator's models. With markups enabled, that spend was also
billed back to the customer, with markup, in the Billed column and the
invoice export. Setting the provider's pricing to zero would have hidden the
estimate the customer wants to see, and a workflow with `budget: false` only
skips the admission check; the spend still accrues.

Issue ENTERPILOT/GoModel#1202 asked for an optional provider on a budget, so
that a budget counts only the provider it names.

## Decision

1. **The provider instance says whether its cost is billable.** A new
   `billable` setting, default `true`, lives in `providers.<name>` in
   `config.yaml`, `<PROVIDER>[_SUFFIX]_BILLABLE`, the dashboard form, and
   `PUT /admin/provider-credentials`. The registry keeps the set of
   non-billable names; declared and dashboard-managed providers register
   through the same call.
2. **The flag is stamped on the usage row.** A tap on the usage logger marks
   every entry served by a non-billable provider, response-cache hits
   included, before the markup tap runs. The row's `billable` column defaults
   to `true`, so rows written before the flag read as billable. Like the
   markup, the value at write time is what counts; changing the flag does not
   rewrite history, and recalculation keeps a non-billable row's billed cost
   at zero.
3. **Billed cost is zero.** With markups enabled, a non-billable row is billed
   at `0` with no markup rule, so billed totals, billed budgets, and invoice
   lines stop charging customers for their own keys while provider cost still
   shows the estimate.
4. **Consumer budgets skip non-billable usage.** User-path and label budgets
   neither gate a request routed to a non-billable provider nor count its
   rows, on either cost basis. Provider budgets still gate and count it, as
   they cap the operator's exposure on that provider.
5. **Failover re-checks what admission skipped.** When a request admitted on a
   non-billable primary fails over to a billable target, the failover sweep
   checks the user-path and label budgets for that target and skips it if one
   is exhausted.

## Alternatives considered

- **A provider field on each budget** (#1202 as filed). It composes with
  several paid providers per customer, but it adds a dimension to the budget
  key, the dashboard, the env var syntax, and per-child templates, and it
  leaves billed cost and invoices wrong for a customer's own key. Billability
  is a property of the provider, not of each budget that might meet it.
- **Reinterpreting workflow `budget: false`** as "do not accrue". It is
  scoped by provider and user path already, but operators use it to exempt a
  privileged subpath whose spend should still count against the parent, so
  the change would silently alter enforcement.
- **A markup of -100% on the provider.** Markup rules are compiled at startup
  from `config.yaml` against the providers known then, so a per-customer
  provider added through the dashboard can never match one, and it would not
  stop the spend from counting against provider-cost budgets.

## Consequences

- The setting is open-core: budgets, cost tracking, and base markups are, and
  the flag makes them correct for bring-your-own-key traffic. Pro features on
  billed cost become correct for it as well.
- A user-path budget no longer measures every priced row under the path; it
  measures what the customer owes. The budgets documentation states this.
- The per-request admission check skips the spend query entirely for a
  non-billable provider; a failover from such a provider to a billable one
  costs one extra spend lookup.
- Per-key billability (an internal or test key whose spend should not count)
  can reuse the same usage column later without a schema change.
