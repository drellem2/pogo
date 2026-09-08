# pgrep's siblings stay visible — and the survey of who still asks it about liveness

**Work item:** mg-fb1c · **Date:** 2026-09-08 · **Status:** Cause of the cause established; corpus surveyed; siblings clause landed in the eight shipped prompts that carry the bullet

mg-cbee established that `pgrep` cannot see pogod from any agent and was archived
still asking *why*. mg-fb1c answered that (`man pgrep`: ancestors are excluded) and
then asked the sharper question, which is the reason this file exists:

> The mechanism was **already shipped verbatim** in `mayor.md` and in a crew memory
> note on the night mg-cbee happened. mg-cbee happened anyway. Why does a rule that
> is true, shipped and read fail to protect the reader?

Because pgrep's **siblings stay visible**. The instrument returns a plausible,
non-empty answer nearly every time it is used, so nothing ever trains distrust —
and the single case it answers wrongly is the case under test.

## 1. The measurement, re-taken from a polecat shell

Taken 2026-09-08 ~09:0xZ from polecat `tfb1c` (pid 81231), not repeated from
mg-fb1c's ticket body. Ancestry first:

```
$ p=$$; while [ "$p" -ne 0 ]; do ps -p "$p" -o pid=,ppid=,comm=; p=$(ps -p "$p" -o ppid=); done
96341 81231 /bin/zsh
81231  6610 claude                                  <- my harness (pogod's child)
 6610  3996 /Users/daniel/go/bin/pogod              <- my grandparent
 3996     1 /Applications/Emacs.app/.../Emacs-arm64-11
    1     0 /sbin/launchd
```

Then the discriminating control — one pattern, one binary, eight processes that
differ **only** in whether they are my ancestor:

```
$ pgrep -f claude
6958 6961 6964 6988 6995 6999 39320 41272 93501 95938

$ ps -Ao pid,ppid,comm | grep '[c]laude'
 6958 6610 claude      architect     VISIBLE
 6961 6610 claude      mayor         VISIBLE
 6964 6610 claude      pa            VISIBLE
 6988 6610 claude      pm-onethird   VISIBLE
 6995 6610 claude      pm-pogo       VISIBLE
 6999 6610 claude      pm-riemann    VISIBLE
41272 6610 claude      polecat t5049 VISIBLE
81231 6610 claude      polecat tfb1c (MY PARENT)   INVISIBLE
```

**7 of pogod's 8 `claude` children are returned. The one that is not is my own
parent.** Same executable, same argv shape (`claude --dangerously-skip-permissions
--append-system-prompt-file …`), same user, same pattern. Ancestry is the only
difference, which rules out "pgrep is flaky here" and rules out anything specific
to the pogod binary.

Ancestors, same instant, with the positive control that separates "absent" from
"filtered":

```
$ pgrep -x pogod        -> (empty)  rc=1
$ pgrep -ax pogod       -> 6610     rc=0     <- control: the process exists and pgrep can name it
```

## 2. Why the siblings are the load-bearing half

An agent that sanity-checks its instrument runs `pgrep -f claude`, sees seven
healthy rows, and concludes pgrep works on this box. It then asks the same
instrument about pogod, gets `rc=1`, and reads it as *the daemon is not running*.

The instrument passes its own smoke test and then answers one specific question
wrongly — **always, on every box, for every agent**. The failure is structural, not
intermittent, so "it worked when I checked" is guaranteed and worthless.

That is the difference between the two clauses:

- *"ancestors are excluded"* — a fact a careful reader can hold and still be surprised.
- *"siblings stay visible, which is what makes the instrument look like it works"* —
  the reason holding the fact does not save them.

The second sentence is what landed in the prompts under mg-fb1c; the first was
already there.

**In fairness to mg-cbee, the observation is not new — the framing is.** Its row in
`docs/investigations/README.md` already records that `pgrep -x claude` "omits this
shell's own parent while listing the other seven", as a generalisation showing the
exclusion is not specific to pogod. What was nowhere — not in `mayor.md`, not in the
crew memory note, not in the prompts a polecat reads — is that this visibility is
*the reason the shipped rule does not protect anyone*. An observation filed as a
generalisation and a sentence placed in the prompt where the mistake is made are
different artifacts, and only the second one is read at the moment it matters.

## 3. The survey: who carries a pgrep-based liveness check

The release condition on mg-fb1c was this survey, which the filing ticket
deliberately did not run. Swept 2026-09-08 across `*.md`, `*.go`, `*.sh`, `*.el`,
`*.fish`, `*.tmux`, `*.py` in the repo, plus the deployed launchd runners.

### 3.1 Shipped prompts that carry the full guidance bullet — 8, all now with the siblings clause

`prompts/mayor.md`, `prompts/crew/doctor.md`, and the six polecat templates
(`polecat.md`, `-qa`, `-triage`, `-build-pr`, `-architect`, `-review`). These are
exactly the eight enumerated by
`TestShippedPromptsWarnPgrepIsNotALivenessInstrument`, which now pins the siblings
clause alongside the mechanism, the `$(pgrep …)` substitution hazard and the
replacement instrument.

### 3.2 THE GAP: `prompts/pm/pm-template.md` carries none of it

Every `pm-*` crew agent is an `extends pm-template` stub, so **no PM prompt on this
fleet carries the pgrep liveness bullet at all** — not the mechanism, not the
substitution hazard, not the replacement.

Measured with a positive control so the zero means something:

```
$ grep -c 'is not a liveness instrument' internal/agent/prompts/pm/pm-template.md
0
$ grep -c 'is not a liveness instrument' internal/agent/prompts/crew/doctor.md
1                                            <- control: the sweep does fire
$ grep -c 'pgrep -f pogo-crew-<your-name>' internal/agent/prompts/pm/pm-template.md
1                                            <- control: pm-template does discuss pgrep, just not this
```

What pm-template does carry is the mg-710c display-label line — a *different*
defect (nothing sets `pogo-crew-<name>` on any process) that resolves to the same
symptom, an empty `pgrep` against a healthy fleet. A reader who has only that line
has been taught the empty result is a naming artifact, which is the wrong lesson to
carry into an ancestry failure.

This is not a hypothetical audience: mg-fb1c's original measurement was taken from
**pm-pogo**, a `pm-*` stub, during a routine sweep. **Not fixed here — noted, and
mailed to pm-pogo and mayor.** Adding a bullet to a prompt that never had one is a
different change from adding a clause to eight that did, and pm-template is shared
by every PM.

### 3.3 Live `pgrep` invocations in the repo — 4, all sound, and exactly 1 in production code

Enumerated from `git ls-files '*.sh' | xargs grep -n pgrep` minus comment lines, so
the count covers tests as well as shipped scripts. No Go file executes `pgrep` at
all — every `pgrep` in `*.go` is a doc comment, an asserted prompt string, or the
`workerenv` note about the display label.

| Site | Call | Verdict |
|---|---|---|
| `scripts/launchd/pogo-reclaim.sh:466` | `pgrep -ax "$name"` | **Correct**, and the only invocation in production code. `-a` was added under mg-19e4 precisely for this; the error direction is an over-count, i.e. a deferred reclaim. |
| `scripts/signal-sender_test.sh:120` | `pgrep -P "$WRAPPER_SHELL"` | **Sound.** The target is the test's own background child — a descendant, never an ancestor, so the exclusion cannot reach it. |
| `scripts/pogo-deploy_test.sh:3061` | `pgrep -P "$pid"` inside `kill_tree_pgrep()` | **Sound and deliberate.** This is the *control*: a local reconstruction of the pre-mg-19e4 walk, kept so the test can demonstrate the undercount it was fixed for. It is not the runner. |
| `scripts/pogo-reclaim_test.sh:287` | `pgrep -x` vs `pgrep -ax` on a real ancestor | **Sound and deliberate.** The premise check that establishes the exclusion is real before the test asserts the fix. |

`scripts/launchd/pogo-deploy.sh` no longer calls `pgrep` at all: `kill_tree` walks
children with `ps` (mg-19e4), and `scripts/pogo-deploy_test.sh:3199` asserts no live
`pgrep -P` call returns to the runner.

### 3.4 The one blind walk still EXECUTING on this box is a stale deployed copy

`~/.pogo/bin/pogo-deploy.sh` — the file launchd actually runs — is dated
**Aug 19 12:00** and still carries the pre-mg-19e4 walk:

```
1563:    for child in $(pgrep -P "$pid" 2>/dev/null); do
```

against a repo copy that has used `ps` since mg-19e4. `diff` between
`main:scripts/launchd/pogo-deploy.sh` and the deployed file is 1263 lines. This is
the known static-copy hazard (the nightly does not refresh its own runner; the
remedy is `pogo service install-deploy`), and it is recorded here only because a
survey of "who asks pgrep about liveness" that reads source alone would report the
defect fixed. **Out of scope for mg-fb1c; not fixed here.**

### 3.5 Prose-only sites (no live call)

`README.md:53`, `MVP.md:138,155`, `ARCHITECTURE.md:1544,1675` — all mg-710c
display-label wording. Go doc comments in `cmd/pogod/main.go:657`,
`cmd/pogo/refineryprogress.go:400`, `cmd/pogod/version_test.go:47`,
`cmd/pogo/pogodhealthline_test.go:13` and `internal/agent/workerenv.go:117` explain
why pgrep is *not* used at those sites. `scripts/test-e2e.sh:427` records a
`pgrep -f "pogo-crew-mayor"` that was removed. No change needed at any of these.

## 4. Not established

- **Linux.** Everything above is macOS (`Darwin 24.6.0`). `-a` is BSD `pgrep`;
  `procps-ng`'s `-a` means "list the full command line" instead, so **`pgrep -a -f
  pogod` must not be written into anything that runs on Linux**. Nothing here was
  run on Linux.
- **Whether the eight prompts change any reader's behaviour.** The mechanism half
  was shipped and did not prevent mg-cbee; that is this ticket's own premise, and
  it applies to the siblings half too. What the clause buys is that the next
  surprised reader has the explanation in front of them rather than in an archived
  ticket. Unmeasured.
- **The deployed-runner drift in §3.4** is reported from `stat` plus `grep` on the
  deployed file and `diff` against `main`. The walk was not exercised.

## 5. See also

- `docs/investigations/pgrep-cannot-see-pogod-2026-08-20.md` — mg-cbee's mechanism
  report, and the `cmd $(pgrep …)` substitution hazard, which none of the fixes
  above disarm.
- `docs/investigations/pgrep-P-undercount-kill-tree-2026-08-20.md` — mg-19e4, the
  `-P` walk that skips its own branch at exit 0.
