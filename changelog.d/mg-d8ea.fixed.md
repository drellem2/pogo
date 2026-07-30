- **The PM roadmap block gains the 7-day window it asks for, and stops letting a
  refused input degrade the Trajectory section silently (mg-d8ea).**
  mg-21b1's `pogo check-prompts` landed ~30s earlier and corrected the two
  refused invocations in `internal/agent/prompts/pm/pm-template.md` — it could
  not enable that gate without fixing the corpus it fails on. This change keeps
  main's corrections and adds the parts a flag-surface check cannot supply.

  **The window is now an invocation rather than an instruction to improvise.**
  Main says the closed status is `done` and that you bound the window yourself
  off `mtime`, which is true and leaves the PM to invent the pipeline at the
  moment it is regenerating a Daniel-facing artifact. There is no closed-at
  field on a work item at all — `mg list --json` emits `created` and `mtime` and
  nothing else temporal — so this is not a flag anyone forgot; it cannot be done
  server-side in any spelling, and it is the reason `Recently shipped (last 7d)`
  had no mechanism behind it. The block now carries the `date`/`jq` form,
  verified on live data (76 done items tagged `pogo`, 69 inside the window).

  **`mtime` is named as the proxy it is.** For a done item it is normally the
  close, but it moves if anyone edits the item afterwards, so a stale item that
  got a tag fix last night reads as freshly shipped. Day granularity (`[:10]`)
  is deliberate: it sidesteps the mixed UTC/offset formats in that field, and a
  roadmap bucket does not need the hours.

  **The silent path is closed in prose, which is the part that actually
  failed.** `pogo-private/docs/roadmap.md@7d07714` (2026-07-29T17:05, the last
  regeneration) has a `## Trajectory` section reporting throughput — "28 merges,
  one release, five polecats still working" — and no token totals and no
  tag-level bottleneck figures, both of which the skeleton at the bottom of this
  same template section specifies. The section was produced without the per-item
  data and said nothing about the omission. What filled the space was dense,
  specific prose, so the gap reads as an editorial choice; anyone auditing this
  by reading roadmaps would have concluded the section was fine. The template
  now says: on a refusal, name in the section which input you could not get,
  rather than improvising an invocation or dropping the section. A corrected
  flag does not fix that — the next refused input for any other reason
  reproduces it.

  **Two faults in this block, two detectability classes.** `--tag`/`--since` are
  unknown flags (exit 2, cobra); `--status=closed` is a legal-looking value mg
  rejects (exit 1, structured JSON error). `pogo check-prompts` documents the
  second as deliberately out of scope — finding it means running the command,
  not reading `--help` — so the `done`-vs-`closed` half of `:535` is pinned here
  by test rather than by the gate. Both errors go to stderr, so neither poisons
  a `--json` pipeline; the failure is a PM seeing an error and deciding what to
  do, which is exactly what went wrong.

  `prompt_test.go` pins the accepted forms, the client-side window, the `mtime`
  caveat, the anti-silent-degradation instruction, and the absence of all three
  refused shapes. The absence assertions are scoped to the ```bash fence rather
  than the file, because the prose has to QUOTE each refused invocation to
  explain it — the same trap mg-4bb9 hit, and the same distinction
  `check-prompts` draws with its `promptcli:retracted` marker. The extractor
  fails the test rather than returning empty if the fence moves, since an
  absence assertion handed an empty string reports the prompt clean having
  examined nothing.
