- **The running coordinator's own prompt still prescribed `mg archive --days=0`
  three hours after the fix for it merged — the deployed file is now repaired in
  place (mg-75ad).** `cad9230` (mg-c2e1) replaced the estate-wide sweep with the
  gated per-id form in `internal/agent/prompts/mayor.md`, and that fix cannot
  reach `~/.pogo/agents/mayor.md`: the deployed file is classified **user-edited**
  (body hash `9627ce51…` against a stamp of `e20f8f69…`, measured by mg-c2e1),
  so `pogo agent prompt install` diverts to `mayor.md.dist` and leaves the
  canonical file untouched. Live line 356 still read *"Archive the work item:
  `mg archive --days=0`"* inside a prescribing bash fence. The one fence was
  replaced with the merged prohibition and the gated per-id form; a
  `mayor.md.bak-<epoch>` sidecar was written first and hash-verified against the
  pre-edit file. `pogo agent prompt show mayor` renders the prohibition, so it
  is what the coordinator loads and not merely what is on disk.

- **Narrow, by Daniel's decision — the other ~149 divergent lines were left
  alone.** The alternative was reconciling the whole deployed file back under
  the installer, which means deciding line by line which divergence is
  deliberate customisation and which is drift; Daniel declined that project
  ("your call, small fish" → narrow). Divergence against `main` fell from 193
  to 149 lines (`diff | grep -c '^[<>]'` — not the same instrument as the 213
  the coordinator reported, which was not re-derived). One diff hunk, confined
  to the archival step. The `pm/pm-template.md` coupling paragraph that sits in
  the merged block was deliberately **not** ported: it references "Recently
  shipped" prose the deployed file does not carry, and importing it would have
  been the reconcile option arriving one paragraph at a time.

- **Verified by reading the fence, because counting inverts here.** The fix works
  by turning a prescription into a prohibition, and prohibiting a command
  requires naming it — so `grep -c 'days=0'` goes **up** across the repair. Both
  the ticket's author and the coordinator hit that trap on this very file (the
  coordinator read 7 on the repo prompt as "still prescribed"). The check that
  answers the question is fence-body extraction: every `mg archive` inside a
  bash fence in the deployed file is now `mg archive <id>` (2 of 2), and all five
  surviving `--days=0` mentions are the prohibition, the `--help` quote, or the
  safe `--dry-run` form.

- **Found and deliberately not fixed: the deployed §Work item archival still
  says the refinery archives automatically.** Line 1157 of the deployed file
  reads *"Once a ticket's code is merged, the refinery archives the work item
  automatically — no action needed from you"* — false since `mg-1f67` removed
  the auto-archive call on 2026-03-26, and rewritten upstream by `cad9230`. It
  is out of this item's scope (one fence, changed nothing else) and it fails in
  the safe direction — it suppresses archiving rather than causing a gate-blind
  sweep — but it is still divergence that a restart will not clear.

- **No code changed and no test was added.** The defect is a state of one file
  on one machine, outside the repo; the repo-side behaviour is already pinned by
  `TestMayorArchivesByIdNotByMassSweep` (mg-c2e1). A test asserting on
  `~/.pogo/agents/mayor.md` would pass or fail on host state and would be red in
  CI, so this fragment is the record instead.

- **The edit is on disk; it is not yet in the coordinator's head.** A running
  agent holds its prompt from load time, so this repair has the same
  merged-not-live shape as the defect it fixes — one level in. Clearing it
  requires `pogo agent stop mayor` (`auto_start = true` restarts it), which is
  the coordinator's own step and not this worker's to take.
