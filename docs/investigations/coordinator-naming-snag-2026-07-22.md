# The coordinator naming snag: which mail, what broke, and who it broke for

*Investigation, 2026-07-22 (mg-04ce). Fact-finding only — no behaviour change, no
prompt edits.*

Daniel recalled "some agent complained by mail that something wasn't working
quite correctly … we may have hit a snag with making mayor the new default and
so we had to put the prompt in a different dir". This is the reconstruction.

The short version: **the defect was real, it is fixed, and it never affected
either Daniel's box or a fresh install.** It affected a third population nobody
named — harnesses that hardcode `mayor` while writing no config. The "different
dir" in the memory is the *defect*, not the workaround.

## 1. Which mail

Daniel's memory compresses **two** mails, and the phrasing inverts the actual
history — the authorized change (mg-ce47) made `ringmaster` the default, *away
from* mayor.

The **"different dir" half** is `human` mail `1784656360098313000.19040.3000`
(mayor, 2026-07-21), *"the crew-crash e2e test had NEVER run…"*, reporting
mg-710c. It is the only mail in the store that ties an unscaffolded prompt
directory to the coordinator-name mismatch. Its architect-facing twin
(`architect/new/1784656384335959000.20919.9000`) states it most crisply.

The **"snag with making mayor the default" half** is `human` mail
`1784759847059993000.39104.3000` (mayor, 2026-07-22) — mayor *refusing* to act
on a "make mayor the default" instruction because it reverses the authorized
mg-ce47, and citing the mg-710c prompt-path failure in the same breath. That is
the actual complaint, and it is still open with Daniel.

**No mail anywhere describes relocating a prompt as a remedy.** Three mails have
the right shape and the wrong cause, and are worth naming so they stop being
re-found: the `mayor.md`/`mayor.md.dist` reconcile thread (17 Jul — a hand-edit
freezing sync, a different *file*, not dir); pogo#15 (`director.md` symlinked to
`agents/` instead of `agents/crew/` — same directory split, wrong agent, May);
and `pa`'s `git init` of `~/.pogo/agents/crew` (7 Jul — literally "put the
prompts in a different dir", unrelated cause).

## 2. What actually broke

`crewPromptPath` (`internal/agent/park.go:187-198`) branches once, on identity:

```go
if name == CoordinatorName() {
    promptFile = filepath.Join(PromptDir(), "mayor.md")   // agents/mayor.md
} else {
    promptFile = filepath.Join(CrewPromptDir(), name+".md") // agents/crew/<name>.md
}
```

`test-e2e` hardcoded the coordinator as `mayor` while writing no config, so
`CoordinatorName()` resolved to the shipped default `ringmaster`
(`internal/agent/prompt.go:30`). The string `mayor` therefore failed the
equality check, fell to the **else** branch, and was looked up as
`agents/crew/mayor.md` — a path `pogo init` never scaffolds. Result: `prompt
file not found`, no coordinator ever started, and step 8 killed whatever sorted
first in the agent list. There is **no fallback**: the branch stats exactly one
path.

Fixed in `d0772f2` (mg-710c), validated in both directions.

## 3. Is it still live? Per population

| Population | Verdict | How established |
|---|---|---|
| **A. Daniel's box** (`[agents] coordinator = "mayor"` pinned) | **Not broken** | Read-only inspection of the live box |
| **B. Fresh install** (nothing pinned, default `ringmaster`) | **Not broken** | Empirically tested in a sandboxed `HOME`/`XDG_CONFIG_HOME`/`POGO_HOME` |
| **C. Harness/CI hardcoding `mayor`, writing no config** | **Was broken; fixed by `d0772f2`** | Code + the mg-710c validation |

**A** resolves `mayor` → `agents/mayor.md`, which exists. **B** resolves
`ringmaster` → `agents/mayor.md`, which `pogo init` writes unconditionally.
Both work because **the prompt filename is a frozen constant that does not
follow the coordinator's name** — so the mg-ce47 default flip is a no-op for
prompt resolution. That is exactly what the design buys.

Only **C** — a caller that supplies a coordinator name the config does not
agree with — hits the else-branch. That is one population, and it is neither
Daniel nor any consumer.

## 4. Coordinator vs crew prompt paths: deliberate

The split is mechanism-vs-policy and is documented in three places
(`internal/agent/api.go:843-845`, `internal/agent/prompt.go:657-661`,
`internal/agent/park.go:185`): *the file name is mechanism and stays put; the
agent's name follows `[agents] coordinator`*. The `DefaultCoordinatorName`
mirror carries a comment explaining why the literal is duplicated
(`prompt.go:25-29`).

One **accidental edge** inside the deliberate design, flagged not fixed: the
name-equality branch precedes any `crew/` lookup and has no fallback, so a
coordinator named after a shipped crew agent (`[agents] coordinator = "doctor"`)
makes `crew/doctor.md` unreachable, and the error names a path the user never
touched. Narrow; no population currently hits it.

Cosmetic residue: under the `ringmaster` default the coordinator is named
`ringmaster` but reads `mayor.md`, and the minimal scaffold says so out loud.
Accurate, confusing.

## 5. pogo#75 — fixed, in code

`stallWatchArmed` (`cmd/pogod/main.go:809-811`) gates the watcher on
`cfg.Source != ""`, wired at `main.go:1373` with an explicit disarm log and a
nil-guarded heartbeat. Commit `3f79fac` (mg-fdd5), confirmed an ancestor of
`origin/main`. Covered by `cmd/pogod/stallwatch_gate_test.go` — a table test
pinning all four combinations plus a boot-direction test that runs the real
binary in a sandbox, explicitly citing the mg-bc47 "predicate right, wiring
wrong" class.

The issue offered two remedies; only the `cfg.Source` gate was taken.
**Population C-adjacent residual:** a *configured* daemon whose coordinator
never starts (`autostart = false`) still mails a stall notice to a mailbox with
no reader. `stallwatch_gate_test.go:84` asserts that on purpose, so it is a
choice, not an oversight. Neither A nor B is affected.

## 6. Refuted: the `/Users/daniel/config.toml` shadowing worry

The 2026-07-10 mail *"v0.4.0 won't kill mayor — the config is ALREADY pinned"*
carried a stated fragility: config resolution prefers `POGO_HOME/config.toml`
(= `/Users/daniel/config.toml`, since this box exports `POGO_HOME=$HOME`) over
`~/.config/pogo/config.toml`, so anything creating that root file without the
`[agents]` pin would silently re-expose the flip.

**That is no longer true, for two independent reasons.**

1. **That path is never a config layer.** `PogoHome()`
   (`internal/config/config.go:929-935`) normalizes a `POGO_HOME` equal to
   `$HOME` to `$HOME/.pogo`. So the POGO_HOME layer on this box is
   `~/.pogo/config.toml`, not `/Users/daniel/config.toml`. Creating the latter
   would be **inert**.
2. **Layering is key-by-key** (mg-cf9e, `config.go:582-585`). `~/.pogo/config.toml`
   *does* exist on this box and carries **no `[agents]` section** — verified — so
   the XDG pin at `~/.config/pogo/config.toml:31` survives unopposed. An
   unpinned higher layer cannot clobber a key it does not set.

The mail predates both fixes. The worry was correct when written and is dead
now; the standing advice not to create `/Users/daniel/config.toml` is harmless
but no longer load-bearing.

## What is still open (not for this ticket)

Daniel's "make mayor the default" instruction reverses the authorized mg-ce47.
That contradiction is unresolved and is his to rule on — mayor has correctly
declined to act on it. Nothing in this investigation depends on which way it
goes: both names resolve to `agents/mayor.md`.
