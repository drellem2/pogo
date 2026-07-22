- **The coordinator naming snag, reconstructed — the defect was real, is fixed,
  and never touched either population we care about (mg-04ce).** Daniel recalled
  an agent complaining that the mayor-as-default change hit a snag "so we had to
  put the prompt in a different dir". The memory compresses two mails and
  inverts the history: the authorized change (mg-ce47) made `ringmaster` the
  default, *away from* mayor, and **no mail anywhere describes relocating a
  prompt as a remedy**. The "different dir" is the *defect*, not the workaround.

  The defect is mg-710c. `crewPromptPath` (`internal/agent/park.go:187-198`)
  branches once, on identity: the coordinator reads `agents/mayor.md`, everyone
  else reads `agents/crew/<name>.md`, with **no fallback**. `test-e2e` hardcoded
  `mayor` while writing no config, so `CoordinatorName()` was `ringmaster`,
  `mayor` failed the equality check, fell to the else-branch, and resolved to
  `agents/crew/mayor.md` — a path `pogo init` never scaffolds. Fixed in
  `d0772f2`.

  **Stated per population, because the answer differs and a finding that does
  not name its population is not usable here.** Daniel's box (`coordinator =
  "mayor"` pinned) resolves to `agents/mayor.md`, which exists — verified
  read-only on the live box. A fresh install (`ringmaster`, nothing pinned)
  resolves to the same file, which `pogo init` writes unconditionally —
  verified empirically in a sandboxed `HOME`/`XDG_CONFIG_HOME`/`POGO_HOME`.
  **Neither was ever broken**, because the prompt filename is a frozen constant
  that does not follow the coordinator's name, which makes the mg-ce47 flip a
  no-op for prompt resolution. The broken population was a third one nobody
  named: harnesses that hardcode a coordinator the config does not agree with.

  The coordinator/crew path split is **deliberate** — mechanism-vs-policy,
  documented at `api.go:843-845`, `prompt.go:657-661`, `park.go:185`. One
  accidental edge inside it, flagged not fixed: the name-equality branch
  precedes any `crew/` lookup, so a coordinator named after a shipped crew agent
  makes that crew prompt unreachable behind an error naming a path the user
  never touched.

  **pogo#75 is fixed in code, not just in ticket state:** `stallWatchArmed`
  (`cmd/pogod/main.go:809-811`) gates on `cfg.Source != ""`, commit `3f79fac`,
  confirmed an ancestor of `origin/main`, covered by
  `cmd/pogod/stallwatch_gate_test.go` in both boot directions.

  **Refuted, and this is the part worth keeping:** the 2026-07-10 mail's stated
  fragility — that `/Users/daniel/config.toml` would shadow the pinned config
  and silently re-expose the flip — is dead twice over. `PogoHome()`
  (`config.go:929-935`) normalizes `POGO_HOME=$HOME` to `$HOME/.pogo`, so that
  root path is **never a config layer** and creating it would be inert; and
  layering is key-by-key since mg-cf9e, so `~/.pogo/config.toml` (which exists,
  and carries no `[agents]`) cannot clobber a key it does not set. The worry was
  correct when written and predates both fixes.

  Record: `docs/investigations/coordinator-naming-snag-2026-07-22.md`.
  Fact-finding only — no behaviour change, no prompt edits.
