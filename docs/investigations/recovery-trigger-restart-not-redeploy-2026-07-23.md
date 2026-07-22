# The fleet-triggerable restart trigger already exists — and must NOT be widened into a redeploy trigger

**Date:** 2026-07-23
**Work item:** mg-cf48
**Verdict:** The trigger exists, is shipped, and is documented — the gap was that no
doc said it activates **zero merged commits**. That is now fixed. Extending it to
redeploy is **mechanically possible but wrong**: three concrete mechanisms in
`pogo-recovery.sh` make a shared queue actively degrade the safety net, and the
purpose conflict is unresolvable in the shared design. **Do not widen it.**
**Xref:** mg-6afa (archived design ruling), mg-6749 (`com.pogo.recovery`),
mg-1bbf (`assert_out_of_band` enforcement, `7187dde`), mg-18d0
(`fleet-auth-expiry-2026-07-22.md`), mg-96ad (held — shipped-prompt half)

## What was measured

Every claim below was verified directly on this host, not inferred from docs.

| Thing | Evidence |
|---|---|
| `pogo recovery request` ships | `cmd/pogo/main.go:2586–2625`, `internal/service/recovery.go` |
| It drops `*.req` temp-then-rename | `EnqueueRecoveryRequest`; `--help` states it |
| `com.pogo.recovery` is a real LaunchAgent | `launchctl print gui/501/com.pogo.recovery` — loaded, `WatchPaths` on the queue |
| The script does `kickstart` and nothing else | `/Users/daniel/.pogo/bin/pogo-recovery.sh`, 105 lines; no `go install` / `go build` / `git pull` / `self-deploy` |
| It is **not** a pogod descendant | launchd-spawned; its plist environment is `PATH`, `HOME`, `POGO_RECOVERY_DIR`, `XPC_SERVICE_NAME` — **no `POGO_AGENT_NAME`** |
| Both arms of the guard hold | `scripts/pogo-self-deploy_test.sh` — **117 passed, 0 failed**, including the reparented-agent arm and the legitimate-caller-proceeds arm |

**The ticket's premise is confirmed.** `pogo-recovery.sh` clears *both* arms of
`assert_out_of_band` — no pogod ancestry, and no `POGO_AGENT_NAME` in its launchd
environment. So it is an eligible out-of-band actor today. Extending it is a
**choice**, not a blocked path. That is exactly why the reasoning below matters:
nothing mechanical stops this, so the argument has to.

## The actual documentation gap (now closed)

`docs/operations.md` already documented tier 3 thoroughly — the CLI, the rate
limit, the `failed/` dir, the plist-path footgun, how to verify it is armed. What
it never said is the one thing that matters most to an agent reading it:

> **it restarts the binary that is already installed, and activates zero merged commits.**

Tier 3 said "Signal a restart". An agent that has just merged a pogod change, and
wants that change live, reads that and reasonably concludes the trigger is for
them. It is not. That misreading is a sufficient explanation for merged pogod work
sitting inert while a working restart trigger was available the whole time.

Fixed in `docs/operations.md` (tier 3 invariants) and in `pogo recovery request
--help`, by **pointing at `scripts/pogo-self-deploy check`** rather than restating
the guard's rule in prose. That follows architect's ruling on mg-96ad: a prose copy
of an executable rule can rot, and the executable copy is the one that fires.

## Why extending it to redeploy is wrong

### 1. Sharing the queue degrades the safety net — three concrete mechanisms

Not a risk-in-principle. These are read straight off `pogo-recovery.sh`:

- **The lock silently drops recovery requests.** `acquire_lock` is a non-blocking
  `mkdir`; on contention the script logs `lock held; another invocation is
  draining — exiting 0`. That reasoning is sound for a peer whose entire runtime is
  one sub-second `launchctl kickstart` — it *will* drain what we would have seen. It
  is **false** for a deploy holding the lock through `do_prove` (~2 min) plus build
  plus drain: a genuine recovery request arriving in that window exits 0 having
  done nothing, and nothing retries it.

- **The 5-minute stale-lock reclaim is tuned to a sub-second script.**
  `STALE_LOCK_MIN=5` exists to reclaim after a crashed invocation. A legitimate
  redeploy routinely exceeds five minutes. A concurrent recovery invocation would
  therefore **reclaim the lock out from under a live deploy and kickstart it
  mid-build** — the fleet-kill-mid-deploy failure `assert_out_of_band` exists to
  prevent, reintroduced one layer up.

- **Coalescing is right for restarts and wrong for audit.** "All `.req` files at
  trigger time are coalesced into a single kickstart" is correct for restarts —
  10 requests genuinely should be 1 restart. Under a deploy it collapses N requests
  into one deploy of whatever `main` happens to be at drain time, which defeats the
  ticket's own acceptance clause: *what revision was activated* is no longer
  answerable from the request record.

### 2. The purpose conflict is unresolvable in a shared path

Recovery exists to rescue a **wedged** pogod. `pogo-self-deploy redeploy` must
**drain** — and a wedged pogod cannot report its polecat count, so the deploy hits
mg-65b2's *"the drain could not establish whether the fleet is idle"* and refuses
without `--force`.

So a deploy wired into the recovery trigger **refuses precisely in recovery's
design case**. The two purposes do not merely have different risk profiles; they
have opposite preconditions. Recovery needs to fire when pogod is unresponsive; a
deploy needs pogod responsive enough to drain.

### 3. mg-18d0 removes most of the motivation

The 2026-07-22 outage was fleet-wide **credential expiry**, not a wedge — and it is
chronic (~5500 synthetic zero-token turns across fleet history). Restart does not
fix that class, and neither does redeploy. Widening the restart trigger invests in
a mechanism now known not to address the dominant failure mode, while spending the
safety net's reliability to do it.

## The failure-visibility question, answered

The ticket demands: how does a failed deploy become visible when the requester is
dead by construction? The honest answer reframes the question.

**The requester is only guaranteed dead *after* the kickstart.** Every pre-kickstart
failure — build failure, quality gates, `do_prove` refusal, drain refusal — leaves
the requesting process **alive and able to report**, because `pogo-self-deploy`
exits with the drain trap armed and the old pogod still running. Those are the
failures a deploy actually has.

The genuinely blind window is narrow: after `kickstart -k`, during verify and the
mail-check post-check. In that window the only readers outside pogod's tree are the
launchd job's own log and its archive dirs.

This is a real answer rather than "the agent mails on failure" — but note what it
implies. The blind window is small **because the deploy script front-loads its
refusals**, which is a property of the deliberate, confirmed, human-invoked path.
It is not a property the trigger would inherit for free.

## Recommendation

**Do not widen `pogo-recovery.sh`. Do not build a parallel deploy queue under this
ticket either.**

If a fleet-triggerable deploy is built later, the safe shape is a **separate
launchd job** — own label, own queue, own lock, own log — sharing nothing with
recovery but the pattern. Never a second verb on the recovery queue. That
separation is what keeps the three mechanisms above from applying.

But it should not be built now, for a reason bigger than this ticket: mg-6afa's
ruling was explicit that redeploy is **"NOT auto-triggered — reported, then human-
or explicitly-invoked"**, and the drain/blast-radius design assumes a caller who can
confirm. Building a Daniel-local launchd deploy queue would prejudge the product
question below with a local-setup answer.

## The product question — surfaced, not built

Daniel: *"perhaps in a generic, productized way for pogo in general so consumers can
consume updates without all this headache."*

This is real and not Daniel-specific: **`mg` self-installs on merge and `pogod` does
not**, so any consumer's daemon silently drifts from its own repo. A consumer has no
equivalent of `scripts/pogo-self-deploy` on their `PATH` and no signal that their
daemon is stale.

The tractable first increment is **drift visibility, not auto-deploy**: `pogo-self-deploy
check` already computes the three-way report (running / installed / `main`) and never
acts. Surfacing that through a shipped surface — `pogo service status`, or `diagnose`
— would tell any installation it is running stale code without anyone deciding, yet,
who is allowed to fix it. That is the cheap half, it is safe from anywhere, and it
does not prejudge the trigger question.

**Recommend a separate design item.** Not built here.

## What was NOT touched

- **`assert_out_of_band` is unchanged.** Both arms re-verified after the doc
  changes: 117 passed, 0 failed.
- **`pogo recovery`'s restart path is unchanged.** No behaviour change in this work
  item at all — documentation only.
- **No shipped agent prompts.** `internal/agent/prompts/**` and `prompt.go` prompt
  strings are untouched; mg-96ad still holds that half pending Daniel's ruling. The
  edits here are repo docs and CLI help, which are not on that red line.
