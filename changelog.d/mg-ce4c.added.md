- **pogo now notices when agents stop reaching their prompt-ready marker.** When a
  coding agent changes its startup output, every spawn silently pays a ready-gate
  timeout — around a minute each, across the whole fleet — and the only evidence
  was one best-effort log line per spawn, in a log nobody reads. The daemon now
  keeps a rolling one-hour window over ready-gate outcomes and, once at least four
  spawns are in the window and more than half of them missed their marker, mails
  the coordinator and records a durable `sentinel_drift` event, somewhere a person
  actually looks. Both markers are covered: the Claude initial-nudge gate and the
  Cursor trust-dialog hook. A spawn whose agent died mid-wait is inconclusive and
  is not counted either way, and alerts are limited to one per marker per episode
  so a drift does not turn into a stream.
