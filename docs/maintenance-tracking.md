# Equipment maintenance tracking

Any counter-based or date-based upkeep — "changed the oil at 55000, next at
65000," "checked the coolant today, redo in 2 years" — can be logged through
the same `memo` tool used for everything else, then queried back with "what's
due?" There's no separate tool: this is four optional fields on a regular
memo (`internal/tools/memo.go`'s `memoRecord`), plus a `maintenance` action
that reads them back across every memo that has them.

## Why no dedicated "vehicle" or "odometer" concept

The four fields are domain-neutral on purpose:

- `metric_name` — a freeform counter name the model (or user) picks per
  piece of equipment: `"odometer_km"` for a car, but just as naturally
  `"main_engine_hours"` or `"generator_hours"` for a boat with more than one
  engine, each on its own independent counter. Nothing in the code assumes
  mileage, or even that a counter exists at all — a purely date-based item
  (smoke detector battery, insurance renewal) just leaves it unset.
- `metric_value` — that counter's value at the time of this record. A record
  can be just a standalone reading ("current odometer is 61000") with no due
  fields of its own — `maintenance` treats the most recently updated memo
  for a given `metric_name` as the last known value, which is how "how much
  is left until the next oil change" gets answered without any live sensor
  anywhere in this system.
- `due_date` / `due_metric_value` — when the next one is due, by calendar
  date and/or by counter value (an item can use either, both, or neither).

This keeps the feature honest about what it actually models: a counter and a
calendar date, nothing more specific — so it fits a car, a boat, a
generator, or anything else with a meter or a service interval, without a
schema change.

## Asking "what's due"

`memo` action `maintenance` scans every active (non-archived) memo carrying
a due field and reports, per item: `days_until_due`/`overdue` (computed in
Go against the real clock, never left to the model to reason about from a
raw date string) and, for counter-based items, `latest_known_metric_value`
and `remaining_metric_value` pulled from whichever other memo most recently
recorded that same `metric_name`. It also returns `known_metrics` — every
`metric_name` seen across all memos, whether or not that particular memo has
a due field — so the model can check it before writing a new reading.

## Keeping one counter from splitting into two names

A weak local model doesn't reliably call `maintenance` before every `write`,
so it can invent a slightly different name for a counter that already
exists — `"odometer"` alongside an already-established `"odometer_km"` —
silently fragmenting one physical counter into two the rest of the feature
now treats as unrelated. `write`'s own response carries the same check
proactively: if the memo just written has a `metric_name` that doesn't match
any other known, non-archived one, the response includes
`existing_metric_names` (every other name currently in use) — normally
absent otherwise. That's timed to land in the same turn's next tool-call
round (the agent loop supports more than one round per turn), so the model
can catch and immediately correct a mismatch it just introduced rather than
leaving two names live. Reusing a name that already exists (or writing the
very first one) never triggers it — the hint only appears when there's a
plausible chance of drift.

## Worked example

```
write: key="oil_change_2026_05", content="changed oil",
       metric_name="odometer_km", metric_value=55000,
       due_metric_value=65000               # 10000 km later, computed
                                             # from the stated interval —
                                             # not left in free-text content
...
write: key="odometer_check_2026_08", content="checked mileage",
       metric_name="odometer_km", metric_value=61000

maintenance ->
  known_metrics: ["odometer_km"]
  items: [{
    key: "oil_change_2026_05", metric_name: "odometer_km",
    due_metric_value: 65000,
    latest_known_metric_value: 61000,   # from the more recent memo above
    remaining_metric_value: 4000,
  }]
```

## Config

No dedicated config — these fields live on the same memo store as
everything else in `docs/memo-search.md`, and `write`'s field validation
(due-date parsing, the 10000-character content cap) is the same code path
regardless of whether maintenance fields are present.
