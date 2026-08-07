# Every exit path in `scripts/pogo-self-deploy`

What the operator is told at each, and whether an install had happened by then.

This table is a deliverable of mg-0155, not documentation for its own sake. The
defect it exists to end is a regress: **the operator-facing story used to be
derived from the exit code, while exit codes are assigned per SITE and stories
are needed per CAUSE.** Every new failure path therefore either reused a code
and silently inherited a story about a different failure, or needed a fresh
integer and a hand-written paragraph. Four generations of that were fixed one
path at a time — mg-8f7e (per-code remedies), mg-65b2 (a discriminator for the
drain-stall alert), mg-0d70 (a new code for the transient sync class), and the
2026-08-07 exit 6 that reported a corrupt install over a run that installed
nothing. Each fix was correct. Each landed exactly where its report pointed. The
next one arrived on a different path.

So the question that ends it is asked once, here, of every path at the same
time: **what is the operator told, and had an install happened by then?**

Writing the table found a defect in the fix that motivated it. See "What this
enumeration caught", below.

## How a failure is reported now

An exit code says **where** the run stopped. That is all an integer can honestly
carry, and it is genuinely useful — it is stable, it is what `launchd` and the
attempt stamp record, and it survives a script that dies before it can say
anything else.

**Why** it stopped travels separately, on the reason record. `pogo-self-deploy`
writes one to `$POGO_DEPLOY_REASON_FILE` on every exit from `redeploy`:

```
exit=6
stage=drain
installed=no
reason=orchestration is STOPPED: POST http://127.0.0.1:10000/agents/drain answered HTTP 503
--- verbatim ---
<every ERROR line the run emitted, in order>
```

`scripts/launchd/pogo-deploy.sh` prints `reason` as the alert's description and
derives "what this attempt changed" from `installed` + `stage`. `describe_exit` /
`remedy_for_exit` remain as the **fallback** for a run that left no record.

Two properties matter more than the format:

- **The record is assembled by the EXIT trap** out of things that accumulate on
  their own — every line `err` emitted, tagged with the stage it was emitted in.
  No exit site has to remember to call anything. A path added next year is
  covered by having been written at all.
- **`installed` is measured, not inferred.** `do_build` sets it to `partial` at
  the mutation point (`go install`) and to `yes` only once every binary has been
  asked its revision and answered `main`. Whether an install happened is a
  property of how far the run got, never of which code it exited with.

## The table

`stage` is the value the record carries. "Installed?" is what `installed=` says
at that point. "Bounced?" is whether `launchctl kickstart -k` had run — the two
come apart on a restart-only deploy, which installs nothing and still bounces.

| Code | Site | stage | Installed? | Bounced? | What the operator is told |
|---|---|---|---|---|---|
| 0 | `--help`, `check` | — | no | no | usage / the drift report. Not a failure. |
| 0 | "nothing owed — pogod is already at main" | startup | no | no | the fleet was not bounced, and why. |
| 0 | `confirm` declined at the prompt | startup | no | no | "aborted by user". |
| 0 | `redeploy complete` | done | yes/no¹ | yes | the full run log, plus the mail-check post-check verdict. |
| 1 | `assert_out_of_band` — caller is inside pogod's tree | startup | no | no | "refusing to redeploy: \<reason\>" — a kickstart would kill the caller. |
| 1 | `resolve_mg` found no usable macguffin | startup | no | no | the paths tried, and that a redeploy with no alert path is refused (mg-015f). |
| 1 | `classify_drift` could not classify | startup | no | no | the `$ACTION` line naming what it could not settle. |
| 2 | `--repo` is not a git repo | startup | no | no | the path, and that it is not a repo. |
| 2 | no repo could be resolved | startup | no | no | "pass --repo PATH or set POGO_REPO". |
| 2 | bad flag / unknown subcommand (`main`) | **none²** | no | no | usage. **No record**: this is before `cmd_redeploy` arms one. |
| 3 | `confirm` with no tty and no `--yes` | startup | no | no | that a fleet-wide bounce will not happen non-interactively. The nightly runner always passes `--yes`, so this reaching an operator means the wrapper is broken. |
| 4 | repo HEAD != the deploy ref | build | no | no | both revisions, and to check out the ref or point `--repo` at one that is on it. |
| 4 | uncommitted changes in the repo | build | no | no | that a dirty tree will not be installed. |
| 4 | staging dir could not be created | build | no | no | that nothing was written to GOBIN. |
| 4 | `go build` into the staging dir failed | build | no | no | the compiler error, and that GOBIN is untouched — this is why the staging build exists. |
| 4 | `go install` failed | build | **partial** | no | the `go install` error. GOBIN may hold some of the set. |
| 4 | a binary's post-install revision != main | build | **partial** | no | which binary, and the two revisions. The install returned 0 over a split pair — the case this loop exists to catch. |
| 6 | drain: `bootstrap` (HTTP 404) | drain | no | no | this pogod predates `/agents/drain`; re-run with `--skip-drain`. |
| 6 | drain: `down` (HTTP 000) | drain | no | no | pogod did not respond at all; start it and retry. Explicitly not the bootstrap case. |
| 6 | drain: `stopped` (HTTP 503) that `/server/mode` does NOT confirm | drain | no | no | both readings — the 503 and the mode the daemon reports — and that it refuses rather than narrate an outage it could not confirm (mg-6d2f). |
| 6 | drain: `error:<code>` (any other status) | drain | no | no | the status, and that this branch deliberately does not guess. |
| 12 | drain: `stopped` (HTTP 503) **confirmed** `index-only` | drain | no | no | **FLEET DOWN.** Orchestration is stopped right now and this run did not restart it; nothing is dispatching. `pogo server start`, then re-run. The refusal itself is correct — a deploy cannot drain a fleet it cannot reach — but it leaves the outage in place, so it does not exit looking ordinary (mg-6d2f). |
| 7 | drain stalled: the budget ran out with polecats still owing the refinery a merge (mg-853a) | drain | no | no | which polecats still hold pushed-but-unmerged work, by name, and the budget; dispatch restored by the trap; `alert_drain_stalled timeout` mails the sink. |
| 7 | drain stalled: state unreadable (`--force` overrides) | drain | no | no | that the fleet's state could not be established — a different reaction from a timeout (mg-65b2); `alert_drain_stalled unknown`. |
| 9 | `do_prove`: live control not found | prove | yes/no¹ | no | that a pogod whose detector cannot be proven will not be deployed. |
| 9 | `do_prove`: no installed pogod / pogo CLI to prove | prove | yes/no¹ | no | which binary is missing, and (for the CLI) that the drain gate shells to it. |
| 9 | `do_prove`: re-entered from inside the control | prove | yes/no¹ | no | that it refuses to recurse. |
| 9 | `do_prove`: the control FAILED on the artifact | prove | yes/no¹ | no | the control's exit code. The best failure in the list: the running pogod was never touched. |
| 9 | `do_prove`: control passed but showed only one direction | prove | yes/no¹ | no | which of RED/GREEN was not demonstrated. |
| 5 | `launchctl kickstart` failed | restart | yes/no¹ | **yes** | that pogod may be DOWN, and the commands to check and restore it first. |
| 8 | `verify_running`: the new pogod never reported main | verify | yes/no¹ | yes | the revision it did report (or "unreachable"). |
| 11 | `verify_orchestration`: pogod came back, but not in `full` mode | verify | yes/no¹ | yes | **FLEET DOWN.** The mode it did report. `/version` is deliberately unguarded, so an index-only daemon answers it at main's revision and passes `verify_running` — this is the half that check cannot see (mg-6d2f). |
| 130 | SIGINT during the drain window | drain | no | no | "interrupted (SIGINT) during the drain window"; the trap restores dispatch. |
| 143 | SIGTERM during the drain window | drain | no | no | "terminated (SIGTERM) during the drain window"; the trap restores dispatch. |

¹ `yes` on a normal deploy, `no` on a restart-only one — `do_build` is skipped
when the installed binaries already match main. This is exactly the column an
exit code cannot carry: the same code, the same site, two different states of
the box.

² The only failing path with no record. `main`'s argument parsing runs before
`cmd_redeploy`, which is where the record is armed. Deliberate rather than
overlooked: an unknown flag is a caller bug, `describe_exit 2` is accurate about
it, and arming the channel earlier would mean arming it for `check`, a
read-only subcommand that never alerts. It is also the live proof that the
fallback path still works.

## Two observations the table makes visible

**Every exit up to and including 9 leaves the running pogod alone.** Codes 1, 2,
3, 4, 6, 7, 9, 130 and 143 — every refusal — stop before the kickstart. Only 5
and 8 can leave the box worse than they found it, and only 5 can leave it
without a daemon. An alert that describes any other code as an outage is wrong,
and that was the whole of the 2026-08-07 misreport.

**Codes 4, 6, 7 and 9 each cover several distinct causes**, and that is fine
*now*: the record distinguishes them. It was not fine while the story was
re-derived from the code. Splitting them into eleven integers would have bought
one quiet night and left the next new path to reuse one of the eleven.

## What this enumeration caught

Building the table found a defect in the mg-0155 fix itself, before it shipped.

The first version of `what_the_run_changed` derived everything from `installed=`,
and rendered `installed=no` as: *"the binaries on disk and the running pogod are
exactly as they were before it started."* True for exit 6 and exit 7. **False for
exit 5 on a restart-only deploy** — no install, `installed=no`, and pogod has
just been killed by a `kickstart -k` that failed. The alert would have told an
operator the daemon was untouched while it was down.

That is the same defect as the one being fixed — one signal asked to carry two
independent facts — reproduced inside its own remedy, one exit path along. The
row for exit 5 is what exposed it: filling in the "Bounced?" column forced the
question the code had not asked. `installed` and `bounced` are now separate, and
`stage_is_past_kickstart` is what separates them.

## Adding an exit path

1. Write the `err` line first, and write it headline-first: the sentence naming
   the cause, then the elaboration. The record takes the **first** err line of
   the stage the run ends in as the alert's description.
2. Call `deploy_stage` if the run has entered a new phase. Nothing else is
   needed — the record assembles itself.
3. Reuse the exit code that says where you are. Do **not** mint a new integer to
   carry a new *reason*; that is the move this document exists to retire.
4. Add a row here. `scripts/pogo-deploy_test.sh` fails if a code appears in the
   script and not in this table, or in this table and not in `describe_exit`.
