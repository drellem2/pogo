- **`pogo check-staleness` now reports what each prompt installer CARRIES, so a
  stale corpus no longer sends the reader at an install that cannot help
  (mg-1e8e).** The prompt witness has correctly reported the gap since mg-dd49
  and has had a runner since mg-385f. What it printed underneath was fixed text:

  ```
  Fix: redeploy, or 'pogo agent prompt install' from a build of the reference.
  ```

  That line names two installers and had read neither. An installer can only
  write the corpus embedded in the binary that runs it, so a prompt corpus is
  **capped at the revision of the process that installed it** — and nothing on
  the box reported that ceiling.

  **What the reading changes, measured 2026-09-08 rather than argued.** mg-1e8e
  was filed on the reading that prompt-install is a THIRD nightly failure mode,
  distinct from the binary install (works nightly) and the pogod restart (fails
  since 09-01, mg-bead) — the argument being that a 17-day-old `mayor.md` beside
  an 11-hour-old binary proves two separate paths. Both halves are wrong:

  - **There is an install step and it is not in the deploy script.** pogod calls
    `agent.InstallPrompts` in-process at every boot. `cmd/pogod/promptrefreshrecord.go`
    already documents this, including why `grep -c 'prompt install'
    scripts/pogo-self-deploy` returning 0 proves nothing.
  - **It ran, and it was right to change nothing.** The `prompt_refresh` event
    stream holds seven runs since 08-22, most recently `2026-09-01T11:07:58Z`,
    every one `ok=true`; the last four read `changed=0 conflicts=[] skipped=[…all
    nine…]`. `~/.pogo/agents` already held `7edd223`'s corpus — the 08-22 boot
    wrote it, which is exactly why `mayor.md` is stamped 08-22 — and every boot
    since has been that same 08-20 binary declining to rewrite its own bytes.

  The prompt path is not a third failure mode; it is **downstream of the restart
  failure**. An unrestarted daemon freezes the prompts at its own build and
  reports `ok=true` doing it, indefinitely.

  **Three rows, because they answer three different questions**, and on this host
  they gave three different answers in the same run:

  ```
  this pogo binary    112dedf0  carries a THIRD version of all 3 — neither the reference nor what is installed
  the running pogod   7edd223c  carries what is ALREADY INSTALLED on all 3 — an install here is a no-op
  the pogod on disk   499eb8a3  carries the reference on all 3 — it CAN close them
  Fix: agent.InstallPrompts at the NEXT pogod boot — it carries the reference content for every file above.
  ```

  So the corpus was 129 lines stale, the installer was healthy, and the remedy
  was a **restart**, not an install. No count of stale files can make that
  distinction.

  **The third cell was found by running the code, not by reasoning.** The first
  version asked one question — does the installer carry the reference content —
  and printed `carries 0 of 3 — it CANNOT close them` over both remaining cases.
  True about the reference, misleading about the installer, and it landed on two
  rows that could not be more different: one carried byte-for-byte what was
  already on disk, the other carried a corpus **newer** than the reference,
  because the default reference is `~/.pogo/deploy-src` and is itself a lagging
  snapshot (the row above it says `BEHIND THE REMOTE`). Collapsing "would change
  nothing" into "would change them to something this report has not judged" is
  the same defect as the fixed Fix line one level up — a verdict wider than the
  reading behind it. The split costs one extra hash comparison against a value
  the deltas already carry, and no additional git or network call.

  **A ceiling is an upper bound, never a forecast**, and the printed block says
  which it is. Content an installer does not carry it cannot write, full stop —
  that half is decidable here and is the half that explains 17 days. Content it
  does carry it may still decline to write: a hand-edited canonical takes
  `InstallPrompts`' conflict cell and gets a `.dist` sidecar, which
  `pogo check-prompt-edits` owns.

  Unreadable installers produce an **UNKNOWN row, never a missing one** — a
  daemon that will not answer `/version` and an unstamped binary have not been
  shown to be capable or incapable, and a witness that drops the reading it could
  not take reports a narrower gap than it measured. The angle-bracketed sentinels
  the revision readers return (`<unreachable>`, `<missing>`, `<unstamped>`) are
  matched before git sees them, so "the daemon is down" cannot arrive dressed as
  a broken reference repo.

  Ceilings are computed only when there are deltas — a ceiling exists to qualify
  a remedy and a clean run prescribes none — and `--skip-ceilings` drops the
  block along with one HTTP call to the daemon and two git reads.

  **The mailed notice carries the same reading, and it is the surface that
  matters.** `internal/promptstale` sweeps on pogod's heartbeat and mails the
  agent reading each superseded file — that mail, not the CLI, is what reached
  the coordinator. It prescribed `pogo agent prompt install` with no statement of
  what the daemon could produce, which on 2026-09-08 pointed the reader at the
  very daemon whose boot installer had just declined to change anything, seven
  times. The sweep now supplies **pogod's own embed** as a ceiling source — free,
  in-process, no git call, no HTTP call, and the correct ceiling for the
  automatic path because pogod *is* that installer — and the notice states which
  of three worlds the recipient is in, judged over that recipient's own files
  only. The warning sits **beside** the install command rather than three
  paragraphs from it, which is asserted by a test: a caveat a reader scrolls past
  before copying the command does not exist.
