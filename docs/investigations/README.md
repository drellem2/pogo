# Investigations

Point-in-time investigation, validation, and calibration reports. Each captures
what was found at a specific date for a specific work item; they are records, not
living documentation. Newer code may have moved on — the report's date and work
item are the anchor.

| Doc | Covers | Outcome |
|-----|--------|---------|
| [architect-crew-agent-evaluation-2026-07-17.md](architect-crew-agent-evaluation-2026-07-17.md) | Should the standing `architect` role ship in the repo as a crew agent at `auto_start = false`? Evaluated per Daniel's "lift it to the pogo repo" / "NOT on by default" | **Recommended NO** — the day-one half of architect's value is review-shaped and **already ships** (`polecat-review`'s architecture lens); the distinctive half is accumulated and can't be boxed ("don't let the box promise the tree"). Ship **mg-945c's `polecat-architect` template** instead — already generic, and it fills design shapes A/B/D that nothing ships today. Found en route: `auto_start=false` puts an agent outside desired state, so `diagnose` **cannot** flag its dead mail loop (`mailLoopFor`) — the requested shape has the fleet's own signature defect in it. Evaluation only, nothing lifted; standing architect and mg-b0cc unaffected (mg-abea) |
| [attach-detach-2026-07-06.md](attach-detach-2026-07-06.md) | Root cause: `pogo attach` Ctrl-\ detach never worked (raw mode clears ISIG, no byte scan) | Root-cause trace + fix (mg-5be3) |
| [attach-terminal-corruption-2026-07-28.md](attach-terminal-corruption-2026-07-28.md) | "Attached a long time, came back detached with control characters in my prompt." | Fixed, **both halves**, neither time-based. (1) **Not an escaping bug — a restore bug**: `term.Restore` puts *termios* back but cannot touch **emulator** state, so every DEC private mode the agent's TUI armed stayed latched on the user's terminal. Claude Code 2.1.220 arms `?1049 ?1000 ?1006 ?1004 ?2026 ?25l` (read as literals out of the shipped binary; `?25` also live in five agents' ring buffers) — and `?1004h` **focus reporting** is the control characters: refocus the window and the terminal types `\x1b[I` at the prompt. Fixed with a *streaming* VT tracker that resets **only modes the agent set** — the blanket-reset fix would send `?2004l` and break the **shell's own** bracketed paste (pinned by test). (2) **The detach is the agent dying under the attach**, not a timeout: `Cleanup` closed the master + listener but left established conns open, so the client froze on a dead socket and was dropped by its *next outgoing byte* — which, with focus reporting armed by (1), is **merely refocusing the window**. "Long attach" is a probability (crew respawn, polecats stop on merge), never a trigger. 4 RED positive controls incl. the reproduction. Attach emits **no events at all** — 177 788 event-log lines, zero real attaches — so Daniel's sessions left no trace; instrumentation surfaced, not built (mg-9b5b) |
| [bridget-fork-2026-05-09.md](bridget-fork-2026-05-09.md) | Forking `cloverross/bridget` for the Discord integration | Done — fork follow-up to the [bridget design](../design/bridget-integration-design.md) (mg-7921) |
| [claude-explore-integration.md](claude-explore-integration.md) | Whether pogo's index needs special config for Claude Code's "Explore" sub-agent | Scoped, deferred (mg-39b6) |
| [codex-e2e-validation.md](codex-e2e-validation.md) | Phase 3D end-to-end validation of the Codex CLI provider | Passed — validation record (mg-6599) |
| [codex-nudge-calibration.md](codex-nudge-calibration.md) | Empirical nudge timing for the Codex provider | Calibration record backing `internal/codex/provider.go` (mg-7f76) |
| [cursor-nudge-calibration.md](cursor-nudge-calibration.md) | Empirical nudge timing + the `.cursor/rules` persona-injection escape hatch for the Cursor provider | Calibration record backing `internal/cursor/provider.go` (mg-c146) |
| [credential-expiry-mechanism-2026-07-23.md](credential-expiry-mechanism-2026-07-23.md) | *Why* the fleet credential expired on 2026-07-21 — the question mg-18d0 explicitly left open, calling recurrence "a matter of time". Also: was it the `/login` or the 2.1.217→2.1.218 auto-update that restored it? | **ESTABLISHED, and PREDICTABLE.** The credential is a Keychain item carrying `expiresAt` + `refreshTokenExpiresAt`: access token **8h**, OAuth refresh grant **exactly 30d and not extended by use**. The prior grant was minted at the item's `cdat` (`2026-06-21T15:17:25Z`) so it lapsed 30d later at `15:17:25Z`; the fleet coasted on its final 8h access token, predicting death by `23:17:25Z` against an observed `23:01:28..23:10:26Z`. **No revocation, no clock event — the designed lifetime, reached.** Corrects mg-18d0's *chronic*: clustering shows **exactly two auth outages ever**, `31d 21h` apart, and its `914`/`498` "two error families" are the **two episodes**, split perfectly clean (`498/0`, then `0/914`) — rate/weekly/spend-limit findings stand, only *auth* is periodic, which is what makes it predictable rather than merely detectable. **Update EXCLUDED**: last failing turn `22:31:05`, credential rewritten `22:31:32`, update `22:36:10` — 5m05s *after* failures stopped; both outages end within 30s of a credential write, neither near a version change. Measured en route: a `/login` does **not** revive running sessions (grant live at `21:31:50Z`, failures until `22:31:05Z`). **Next death, absent a `/login`: `2026-08-21T21:31Z .. 2026-08-22T05:31Z`** — so warn *before* rather than detect *after*; does NOT replace mg-8cdb (early revocation is reactive-only). Live credential not experimented on (mg-ed45) |
| [deaf-survivor-off-by-default-2026-07-17.md](deaf-survivor-off-by-default-2026-07-17.md) | An `auto_start=false` agent someone turns ON is a DEAF SURVIVOR: `mailLoopFor` returns UNKNOWN **before any lookup** (`IsExpectedAgent` gates it), so a dead mail loop can never report MISSING — mg-de08's pathology in the population de08's bar excludes | Fixed — `diagnose` now judges a CONFIGURED **and RUNNING** agent, not just an EXPECTED one: mg-8677's *evidence beats expectation* one consumer over. RED demonstrated first (`Health = "healthy"` on a live deaf agent), then positive + conditional controls (a genuinely-absent agent still reports UNKNOWN). Reap confirmed NOT implicated (registry evidence wins before `DesiredStateFor`); `doctor` + `pm-lineara` ship this config today. **Excluded populations named out loud** — polecats (witness's job), not-running agents, and anything nobody runs `diagnose` against: this makes the fault detectable, not announced. stop/park half SPLIT (already shipped as mg-ce26) (mg-738f) |
| [investigation-mg-06f2.md](investigation-mg-06f2.md) | Root cause: tickets archived "done" before the refinery confirmed the merge | Root-cause trace (mg-06f2) |
| [launch-readiness-audit-2026-03-21.md](launch-readiness-audit-2026-03-21.md) | v0.2 launch-readiness audit across install, agents, refinery, release | Point-in-time audit, 2026-03-21 — no hard blockers |
| [nudge-claude-code-workaround.md](nudge-claude-code-workaround.md) | Workarounds for nudging Claude Code through mid-session modals | Investigation; the modal watcher it scopes since shipped (mg-4421, `internal/claude/modal_hook.go`) |
| [phantom-polecats-from-go-test-2026-07-17.md](phantom-polecats-from-go-test-2026-07-17.md) | `go test ./internal/agent/` wrote PHANTOM polecats into the LIVE witness store — real test-process pids under Go fixture names (`ready-test`, `cadence`, `no-sentinel-profile`) — and pogod mailed the mayor an authoritative `kill <pid>` for each, at pids already dead and recyclable; 3 mails in 10 minutes | Fixed, **both halves**. (1) The store is test-safe **by default**: under `testing.Testing()` the live path is not reachable from `WitnessPath()` at all. Deliberately NOT more `sandboxWitness` calls — that is the opt-in guard that already failed, and the distribution is the finding (`witness_test.go` sandboxed **16×**, the two files that polluted the fleet **0×**, because they spawn agents while testing *nudges* and *attach*): **an opt-in guard is only ever remembered by the tests that least need it** (mg-a558's shape; same pollutant as `events.log`). (2) The alert is re-verifiable at READ time: mg-13a3's `(pid, start_time)` protects the DETECTOR from pid reuse but was stripped from the MAIL, so it never reached the one consumer told to run `kill` — every runnable kill is now **gated** on `pogo agent witness --json \| grep -q '"name":"x","pid":N'` (both halves of the identity: a name-only grep passes against a live *successor*), pinned against the real command's real output. Alert deliberately **not** silenced — the noise was the bug, not the alarm. 4 RED proofs, incl. the incident reproduced verbatim; live store byte-identical after a full suite run (mg-da48) |
| [spawn-polecat-rc0-and-poisoned-branch-2026-07-17.md](spawn-polecat-rc0-and-poisoned-branch-2026-07-17.md) | Three defects reported against `spawn-polecat`: exits 0 on failure; a failed spawn leaves a branch with no worktree that poisons every retry; pogod emits no spawn-failure event. Plus an n=1 same-repo concurrency race | **rc=0 REFUTED — it is the HARNESS**: the wave issued spawns with `&` + bare `wait`, which **returns 0 regardless of the job's status** (pipelines to `tee` likewise). Direct invocation exits **1**; so does a **four-month-older** shadowed binary — *no build on the box exhibits rc=0*, and the "stale binary" story is impossible (the binary predates the observation). **Second refutation of the same claim** (gh #28/mg-a1e4, 2026-07-03, "does not reproduce") — it recurred because that one lived only in a **commit message**, so this doc names the *mechanism* mg-a1e4 lacked. **Voids the rc=0 half of the mg-eb54 → mg-3c32 gate.** The other two are REAL and fixed: `git worktree add -b` creates the branch *then* checks out, so a failed add strands it (reproduced **deterministically**, no race needed) — spawns now reclaim a *provably spent* orphan (**55 existed**, from ordinary *merged* work, breaking the mayor's own documented stop→unclaim→re-dispatch recovery) and refuse to touch a live or unmerged one; and `agent_spawn_failed` now fires on every failure path incl. the drain **throttle**, closing the absence that produced eb54's false "the cap throttled it" finding (**0 failure events in 34,090 spawns**). Race **still unproven** — but no longer *needed* to explain anything, and now bounded to transient + self-announcing (mg-d22a) |
| [pi-nudge-calibration.md](pi-nudge-calibration.md) | Empirical nudge timing + persona/trust integration for the pi provider | Calibration record backing `internal/pi/provider.go` (mg-9829) |
| [pogod-shutdown-stops-nothing-2026-07-17.md](pogod-shutdown-stops-nothing-2026-07-17.md) | pogod's `defer StopAll` on shutdown: does it ever run? Proven unreachable on EVERY exit path (SIGTERM has no handler; the only other exit is `log.Fatal` — both skip defers), so the PTY hangup is the real mechanism | Fixed — the `defer` deleted as unreachable and the hangup documented in its place; zero `stopped` lines on SIGTERM against a live positive control. `StopAll` itself KEPT: it has a live caller (`transitionToIndexOnly`), contra the ticket's "dead code" premise (mg-6b66) |
| [orphaned-polecat-2026-07-17.md](orphaned-polecat-2026-07-17.md) | What resolves an `AgentUnknown`? mg-13a3 stopped the reap but left the survivor in permanent limbo — alive, unreachable, holding a worktree and a claim, mail-check firing into a void | Answered + fixed — **a human resolves it**: adopt is impossible (the PTY master died with its pogod) and would relocate the lie into the registry; kill makes absence authoritative for destruction (mg-de08 mirrored). So survivors are enumerated and surfaced (durable event + coordinator mail, repeating until the fault clears), and the drain stops printing "0 polecats active" over them — reported, deliberately NOT counted. 7 RED proofs; the fix rebuilt the bug inside itself and its own test caught it (mg-0b77) |
| [polecat-witness-2026-07-17.md](polecat-witness-2026-07-17.md) | Giving polecats the second witness crew get from `auto_start`: persisted `(pid, start_time)` so registry-absent + OUR pid alive is UNKNOWN, never GONE — the fix mg-61a0's repro called for | Fixed — the reap no longer concludes death from two absences; four RED proofs recorded, incl. the required recycled-pid control (mg-13a3) |
| [wake-watcher-mechanism-confirmed-2026-07-21.md](wake-watcher-mechanism-confirmed-2026-07-21.md) | Does mg-55de's reaper address the real mechanism? Re-opened because the live pogod had **not died once** since boot, yet the orphan age histogram showed repeated cohorts (`07h:114`) — reading as repeated *spawns*, which would make the reaper a guard downstream of a source that keeps producing | **CONFIRMED — parent death is the sole mechanism; question closed.** The dichotomy was false: the one production call site (`main.go:1532`) spawns exactly one watcher per pogod boot and strands *that* watcher on *that* pogod's death, so a multi-cohort histogram is the **signature** of parent death, not evidence against it. The deaths were never the long-lived daemon's — they were the many short-lived pogods tests and deploys boot and kill; the live daemon's own watcher stayed singular and stable since boot. Proven by **reproduction in a sandbox**, not inspection: SIGTERM a pogod → its watcher strands at PPID 1; boot another → it reaps the orphan and **spares** the live one. The reaper is **not** downstream — it runs inside `Watch()` *before* the spawn, so the source is its own trigger and cannot outrun it; the 242 accumulated because no pogod had ever reaped. Alternatives worse (a SIGTERM handler misses SIGKILL/panic/crash; darwin has no PDEATHSIG; SIGPIPE fails because this child almost never writes). The `Watch(context.Background(), nil)` attribution refuted for the sharper reason: **production's `hbCtx` is equally uncancellable**, since no exit path calls its cancel — the defect was never a test's bad context. Fact-finding only; reaper still **inert until redeploy** (mg-c3a6) |
| [pty-investigation-2026-05-09.md](pty-investigation-2026-05-09.md) | PTY rendering glitches on `pogo agent attach` | Read-only investigation; fix carried by a follow-up ticket (mg-098c) |
| [rating-dialog-match-2026-07-13.md](rating-dialog-match-2026-07-13.md) | Root cause: rating-dialog marker never matched the real TUI footer (column-move escapes collapse spaces under `StripANSI`) | Root-cause trace + fix — whitespace-insensitive matching (mg-f36b) |
| [refinery-rebase-regate-2026-07-17.md](refinery-rebase-regate-2026-07-17.md) | Does a rebased 2nd+ PR in a batch get RE-TESTED post-rebase? — read so the nightly isn't rested on an unchecked premise (sharpens or softens mg-bfe5) | Answered — it DOES re-run: the 2nd PR's gate ran against a tree containing both PRs, at the exact SHA that became main's HEAD. `attemptMerge` orders rebase→gates→ff-only→push; ff-only forces a re-gate if main moves mid-gate. Latent `skip_on_retry` hole reported, OFF for pogo. Fact-finding only, no fix (mg-a9bb) |
| [recovery-trigger-restart-not-redeploy-2026-07-23.md](recovery-trigger-restart-not-redeploy-2026-07-23.md) | The fleet-triggerable restart trigger was believed missing and had to be built. Does it already exist, and should it be widened from restart to redeploy? | **It already exists** — `pogo recovery request` + the `com.pogo.recovery` LaunchAgent, shipped and documented. It is launchd-parented with **no `POGO_AGENT_NAME`** in its plist env, so it clears *both* arms of `assert_out_of_band`: widening it is a **choice, not a blocked path**. **Recommended AGAINST.** Three mechanisms read off `pogo-recovery.sh` make a shared queue degrade the safety net: the non-blocking lock **silently drops** a recovery request under a minutes-long deploy (sound only for a sub-second peer); `STALE_LOCK_MIN=5` would **reclaim the lock out from under a live deploy and kickstart it mid-build**; and coalescing defeats *which revision was activated*. The purposes have **opposite preconditions** — recovery needs pogod unresponsive, drain needs it responsive — so a deploy on this trigger refuses precisely in recovery's design case. mg-18d0 removes the motivation (credential expiry, which neither fixes). Failure visibility reframed: the requester is only dead *after* the kickstart; every pre-kickstart refusal leaves it alive. Real gap was documentation — no doc said tier 3 activates **zero merged commits**; fixed in `operations.md` + CLI help by pointing at `pogo-self-deploy check`. Guard untouched, both arms re-verified 117/0. Product question (consumers' pogod drifts; `mg` self-installs, `pogod` does not) surfaced for its own item, not built (mg-cf48) |
| [redeploy-drain-2026-07-17.md](redeploy-drain-2026-07-17.md) | What `pogo-self-deploy`'s drain actually does: waits or kills? — read for f206's unattended-redeploy arming decision | Answered — the drain WAITS (1800s, fails closed) and never kills; `--force`'s "kill" is measured to be mg-61a0's PTY accident, not `kickstart -k`. Fact-finding only, no fix (mg-46a4) |
| [registry-absent-while-alive-2026-07-17.md](registry-absent-while-alive-2026-07-17.md) | Root cause: the registry reports a live agent ABSENT after any pogod restart it outlived (no lock window); a live polecat's mail-check is then reaped — reproduced end-to-end | Reproduced — only an accidental PTY hangup prevents it; invariant pinned by test, harness-independent fix deferred (mg-61a0) |
| [renudge-efficacy-2026-07-14.md](renudge-efficacy-2026-07-14.md) | Efficacy of the bare-CR auto-renudge against a real Claude Code paste-buffered kickoff wedge; head-to-head vs. the field-confirmed `"1"`+CR | Verified — bare CR recovers 8/8, `"1"`+CR 5/5; bare CR preferred (no stray char), no `"1"` fallback needed (mg-feb3) |

## Audit of recommendations and verdicts — 2026-07-29 (mg-d489)

Every doc above was read against one question: **it reached a conclusion — is anything
carrying that conclusion forward?** A conclusion reached in a doc lives outside the
work-item store, which is where conclusions go missing. Two ways they go missing:

- **Unfiled recommendation** — the doc recommends work and nobody filed it.
- **Orphaned answer** — the doc answers a question some ticket body still asks, unamended.

Both were proven, from one document, before this audit ran.
`recovery-trigger-restart-not-redeploy-2026-07-23.md` produced each once: its verdict
(*"Do not widen `pogo-recovery.sh`"*) was an orphaned answer — mg-cf48's body still asked
it, and polecat mg-6d09 was dispatched to re-derive a decision already shipped — and its
closing *"Recommend a separate design item. Not built here."* sat unfiled for six days
(now mg-75ec, the only consumer-facing item in the whole self-deploy arc). Both are fixed;
they are why the predicate was believed worth running.

### Method, and what it cost

    docs/investigations/*.md                                  : 31
    grep -ilE 'recommend|do not|verdict|ruling'                : 26   <- candidates
    read, and found to carry a real conclusion                 : 23
    grep false positives                                       :  3

**A grep cannot tell a recommendation from a mention of one, and 3 of 26 were mentions.**
`README.md` matched on the verdicts it *quotes* from other docs — it is this index, and
holds no conclusion of its own. `pi-nudge-calibration.md` matched on a quoted Cursor TUI
string (`Do not trust`). `bridget-fork-2026-05-09.md` cites an architect ruling made
elsewhere (mg-7921) and records work already done. **3/26 is a tight predicate, not a
loose one** — a tighter next pass would gain little and would risk dropping docs whose
recommendation is a single line in a §Scope section, which is where two of this pass's
four findings were.

### Results

| Doc | Conclusion (one line) | Status | Action |
|---|---|---|---|
| architect-crew-agent-evaluation-2026-07-17 | NO — do not lift `crew/architect.md`; ship mg-945c's `polecat-architect` instead | Carried | none — rec 2 → mg-564c, rec 4 → mg-738f, rec 1/3 honoured |
| bridget-fork-2026-05-09 | *(cites a ruling, makes none)* | **False positive** | none |
| claude-explore-integration | Option 3 — no config needed today; an MCP wrapper without a semantic index is cargo-culting | Carried | none — deliberately unfiled with a named trigger (*"raise it if observed Explore behaviour is poor"*) |
| codex-e2e-validation | Phase 3D passes; two auth items are Daniel's | Carried | none — mg-4bb2 (`decision1-resolved`), item 2 → mg-b31b |
| coordinator-naming-snag-2026-07-22 | Real, fixed, and it never hit Daniel's box or a fresh install — only a harness hardcoding `mayor` while writing no config | **Mixed — 3 actions** | filed **mg-4469**, filed **mg-2c17**; appended to **mg-04ce**; pointer for **mg-ace6** blocked (below) |
| credential-expiry-mechanism-2026-07-23 | The grant's 30d lifetime was reached, not revoked — next death `2026-08-21T21:31Z` | Carried | none — rec 1 → mg-7024, rec 4 → `docs/operations.md` |
| cursor-nudge-calibration | An offline e2e is not achievable at proportionate cost for the `agent` binary | Carried | none — mg-c146; follow-up is mg-cdb6 |
| deaf-survivor-off-by-default-2026-07-17 | Fixed — `diagnose` now judges a CONFIGURED **and RUNNING** agent | **Unfiled** | filed **mg-032b** — the excluded population it named ("an agent nobody runs `diagnose` against") never got a ticket |
| fleet-auth-expiry-2026-07-22 | CONFIRMED — credential expiry, not a wedge; the SPOF was real but not the cause | Carried | none — rec 1 → mg-8cdb, recs 3–4 → mg-1935 |
| investigation-mg-06f2 | Tickets archived `done` before the refinery confirmed the merge | Carried | none — Fix 2 → mg-34da, Fix 1 shipped (`cmd/pogod/main.go` auto-reopen), Fix 3 is the live polecat protocol; Fix 5 was optional and remains unbuilt **by choice** |
| launch-readiness-audit-2026-03-21 | No hard blockers; two areas NEEDS_REVIEW | Carried | none — all three "Should Fix Soon" resolved (worktree race mg-0779/mg-0d09; `GateOutput` now written under `r.mu`; history capped by `MaxHistoryLen`) |
| launchd-nondemand-spawn-postreboot-2026-07-21 | Post-reboot, both `StartInterval` and `StartCalendarInterval` fire — sleep is the trigger, not the machine | Carried | none — the "cheap next measurement" is mg-01f7 |
| nudge-claude-code-workaround | Candidate 1 is a real workaround whose necessity is empirically testable; candidate 2 is not a workaround at all | **Unfiled** | filed **mg-68c8** — §3's protocol was specified, mg-09b6 archived with *"polecat runs section 3"* as its next action, and nobody ran it |
| orphaned-polecat-2026-07-17 | A human resolves an `AgentUnknown`; survivors are surfaced, deliberately not counted | Carried | none — mg-0b77; the adjacent `cleanup_orphans` defect it reported → mg-a558 |
| phantom-polecats-from-go-test-2026-07-17 | Fixed both halves: the store is test-safe by default, and the kill alert is re-verifiable at read time | Carried | none — mg-da48; the recurrence → mg-b399 |
| pi-nudge-calibration | *(matched a quoted TUI string)* | **False positive** | none |
| pogod-shutdown-stops-nothing-2026-07-17 | The `defer StopAll` was unreachable on every exit path; deleted, hangup documented in its place | Carried | none — mg-6b66. Residue below. |
| polecat-witness-2026-07-17 | The reap no longer concludes death from two absences | Carried | none — mg-13a3; the gitgc follow-up it split off → mg-0130 |
| pty-investigation-2026-05-09 | PTY at 0×0 → Ink falls back to 80×24 and renders at the wrong size | Carried | none — mg-5564 |
| recovery-trigger-restart-not-redeploy-2026-07-23 | It already exists; **recommended AGAINST** widening restart to redeploy | Carried | none — the two proven instances, both already fixed (mg-cf48, mg-75ec) |
| redeploy-drain-2026-07-17 | The drain WAITS (1800s, fails closed) and never kills | Carried | none — §5(a) → mg-0b77/mg-65b2, §5(b) → mg-8b48, §6.3 → mg-f206 |
| refinery-rebase-regate-2026-07-17 | It DOES re-gate; `skip_on_retry` is a latent hole and OFF for pogo | Carried | none — *"No ticket filed"* is the doc's own reasoned choice, on a config pogo does not use |
| renudge-efficacy-2026-07-14 | Bare CR recovers 8/8; no `"1"` fallback needed | Carried | none — the live-wave follow-up gate is mg-eb54 |
| spawn-polecat-rc0-and-poisoned-branch-2026-07-17 | rc=0 **REFUTED** — it is the harness's `&` + bare `wait`; the other two defects are real and fixed | Carried | none — mg-d22a; the refutation is recorded in both bodies that relied on it (mg-eb54, mg-3c32) |
| wake-watcher-mechanism-confirmed-2026-07-21 | CONFIRMED — parent death is the sole mechanism; question closed | Carried | none — mg-c3a6; "inert until redeploy" resolved by mg-42ac/mg-b7d0 |
| README.md | *(this index; quotes others' verdicts)* | **False positive** | none |

### Filed

| Item | From | Why it had no carrier |
|---|---|---|
| **mg-032b** | deaf-survivor §"populations this fix excludes" | `MailCheckMissing`'s only consumer is `pogo agent diagnose <name>` — verified today, one call site. mg-1935 (ackwatch) is adjacent but reads schedules that **exist**; an agent with no mail-check has no counter row. Detectable, never announced. |
| **mg-4469** | coordinator-naming §4 | `crewPromptPath`'s name-equality branch has no fallback, so `[agents] coordinator = "doctor"` makes `crew/doctor.md` unreachable and the error names a path the user never touched. Latent — no population hits it — which is when it is cheap. |
| **mg-2c17** | coordinator-naming §"What is still open" | Daniel's *"make mayor the default"* reverses the authorized mg-ce47. Mayor correctly declined to act; the contradiction has lived in a `human` mail thread since 2026-07-22 with no work item. `assignee=human`. |
| **mg-68c8** | nudge-claude-code §3/§5 | The 50ms `SubmitDelay` is still shipped and still inherited rather than measured; the protocol to settle it was written and archived unrun. |
| **mg-d878** | *(found by this audit's own remedy)* | See below. |

### Amended

**mg-04ce** — its Constraints section said creating `/Users/daniel/config.toml` *"would actively
break his install."* The investigation it commissioned refuted that (`PogoHome()` normalizes a
`POGO_HOME` equal to `$HOME`, so the path is never a config layer; and layering is key-by-key).
Appended, not rewritten.

### One pointer could not be written — and the blocker is now the finding

**mg-ace6** ("stall-watch not gated on `cfg.Source`", pogo#75, shelved) is **fixed** — `stallWatchArmed`
gates on `cfg.Source`, commit `3f79fac` (mg-fdd5) is an ancestor of `origin/main`, and
`cmd/pogod/stallwatch_gate_test.go` is the regression test its body asks for. Both acceptance
criteria are met. Its body still reads as an open bug.

`mg edit mg-ace6 --append-body-file` is **refused**: the item carries the `gh-issue` tag and its body
predates the carrier block, and `reconcileWorkflowMarkers` validates the effective tag set against the
resulting body on every write. There is no append-only escape — `leadingWorkflow` requires the
`workflow:` line to open the body's leading block, so a carrier block inside appended text is
`misplaced` and refused outright. **41 of 92 `gh-issue` items are in this state; 7 are still live.**
Filed as **mg-d878** (macguffin). The pointer went to pm-pogo by mail; mg-ace6's body was **not**
rewritten, because capture-then-rewrite is what mg-f326 exists to prevent.

### Reported, deliberately not filed

- **`pogod`'s `defer lock.Unlock()` (`cmd/pogod/main.go`) is dead for exactly mg-6b66's reason** —
  SIGTERM has no handler and the only other exit is `log.Fatal`, so it never runs. The doc named it
  ("out of scope for this ticket, but it is the same fact"). Checked before dismissing: it is
  **harmless**, because `nightlyone/lockfile`'s `TryLock` deletes a lockfile whose recorded pid is
  dead. Cosmetic residue of a deletion that stopped one line short.
- **`docs/` outside `investigations/` was not swept.** Out of scope for this pass by instruction. No
  claim is made about it either way.

### Bound — read this before treating the table as a clean bill of health

**This pass covers answers that landed in a DOC.** Mayor's population is larger: *a record that
framed a question and was never amended when the answer landed elsewhere.* The half where the answer
landed in **another ticket** has no structural signal and no terminating predicate — mg-cf48 was
correctly `done`, correctly tagged, and owed no successor, and no type check, tag check or `mg` guard
catches that shape. **That half stays open.** A clean docs audit says nothing about it.

Two instances of it were seen in passing while working this ticket and are recorded here rather than
chased, because each needs a judgement its owner should make:

- **mg-18d0** (`done`) says the stale-heartbeat→restart rung is *"still missing, still worth
  building."* No successor carries it. Whether it should be built is genuinely open — mg-18d0 itself
  argues it must be justified on its own merits and not on the 07-22 outage.
- **mg-eb54**'s title says the wave verify *PASSED*; the architect gate ruling at the foot of the same
  body downgrades that and names three unmet conditions. Both are in the body; only one is in the
  title, and the title is what a queue listing shows.
