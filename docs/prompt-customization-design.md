# Pogo prompt customization design

**Status:** proposal · **Owner:** architect · **Tracks:** mg-7488
**Date:** 2026-05-08

## Context

Daniel directive 2026-05-08: *"it would be nice to have a convention or something where pogo agent prompts are customized in a safe way that doesn't get overridden on `pogo install --force`. Can work with architect to design a good Unix principles solution."*

Today a user who edits `~/.pogo/agents/mayor.md` (or `templates/polecat.md`, or `pm/pogo.toml`) for their own workflow loses those edits the next time they run `pogo install --force`. They lose them silently on a non-`--force` install too, the moment the embedded prompt changes between pogo versions. The protection that exists is a hash stamp that detects whether *the shipped version has changed*, not whether *the user has edited the file* — so user edits get steamrolled even when the install code thinks it is being polite. This ticket designs a way to keep customizations safe without forking the prompt directory.

Design-only. Implementation tickets are filed as follow-ups once Daniel picks an option.

## Diagnosis

Survey of `cmd/pogo/main.go:842–892` and `internal/agent/prompt.go:849–993`:

- **Install model is hash-stamp + skip-or-overwrite.** `InstallPrompts` (`prompt.go:939–993`) walks the embedded `prompts/` tree, prepends a `<!-- pogo-prompt-hash: <sha256> -->` stamp (`prompt.go:730–739`) recording the hash of the *embed at install time*, and writes to `~/.pogo/agents/`. On the next install: `embed_hash == stamp` → `Skipped`; `embed_hash != stamp` → `Updated` (overwrite); `--force` → always `Installed` (overwrite). The stamp is content-of-embed, not content-of-installed-file, so an in-place user edit is invisible to the gating logic.
- **Files in scope.** Embed currently ships `mayor.md`, `crew/doctor.md`, `pm/pm-template.md`, `templates/polecat.md`, `templates/polecat-qa.md`. PM scope configs (`pm/*.toml`) are also written and stamped through the same path. Per pm-pogo's `reference_live_agents.md`, live state — `pm/<instance>/memory/`, `pm/<instance>/sweep.log`, user-created `crew/<name>.md` — is already exempt because `InstallPrompts` is copy-only, never delete-sync.
- **One layering primitive already exists.** The `extends <template> with config <toml>` directive (`prompt.go:95`, synthesised by `SynthesizeExtendsPrompt` at `prompt.go:108–157`) lets a per-PM crew prompt point at the shared `pm-template.md` plus a per-instance config TOML. This is exactly the pattern we want to generalise — *user customizations live in a separate, install-untouched file that composes onto the shipped base* — but today it only covers the PM-template/PM-config split.
- **Nothing else is layered.** Greps for `local/`, `conf.d`, `dropin`, `.dist`, `override` (in the layering sense), `XDG_*` come back empty. There is no precedent for a user-side customisation slot for `mayor.md` or `polecat.md`.
- **First-class problem cases.** (1) Add a house rule to mayor (purely additive). (2) Replace a polecat-template instruction (replacement, not addition). (3) Tweak `pm/pogo.toml` scope (already in a separate file but still hash-stamped and overwritable). The design has to cover all three cleanly; (1) is the easy case, (2) is the one that breaks pure additive layering.

## Proposal

Two mechanisms, both small, designed to compose: (A) a primary *drop-in* convention so users can customise additively without ever touching the shipped file, and (B) a *conflict-detection safety net* so users who do edit the shipped file never lose their work silently. (A) is the recommended path. (B) covers the "didn't read the docs" case and the cases (A) cannot express.

### A. Drop-in directories under `~/.pogo/agents/dropins/`

For each shipped template, the user can drop overlay files into a sibling directory:

```
~/.pogo/agents/
├── mayor.md                          # shipped, hash-stamped, overwrite candidate
├── dropins/
│   ├── mayor/
│   │   ├── 00-house-style.md         # appended to mayor.md at synthesis time
│   │   └── 90-late-rules.md
│   ├── polecat/
│   │   └── 50-extra-claim-rules.md   # appended to polecat.md before {{.Var}} expansion
│   └── pm-template/
│       └── 20-mailroom-policy.md
├── templates/
│   └── polecat.md                    # shipped, hash-stamped
└── ...                               # everything else unchanged
```

Composition rules:
- At prompt-load time (`ResolveTemplate` and the `SynthesizeExtendsPrompt` path), after the base file is read, the loader checks for `~/.pogo/agents/dropins/<basename>/`. If present, all `*.md` files inside are read in lexical order and appended to the base. Lexical order is the well-known systemd / cron.d convention; numeric prefixes give the user explicit control without adding a config knob.
- For TOML configs (`pm/*.toml`), drop-ins use the same directory shape (`dropins/pm/<instance>/*.toml`) and are merged later-wins on top of the base — same pattern as systemd unit overrides.
- `InstallPrompts` and `--force` *never* touch `dropins/`. The directory is wholly user-owned.
- `pogo agent prompt show <name>` (existing command path or new) prints the synthesised result so users can verify their drop-ins compose how they expect.

Drop-ins are additive-only by design. That handles cases (1) and (3) above, and the common shape of (2) — *append a stronger replacement instruction at the end* — without giving users a way to silently delete a shipped rule. Pure replacement (deleting a shipped rule outright) lands in mechanism B.

### B. Conflict-detection safety net on the canonical file

The existing hash stamp records the embed-hash at install time. Add a second hash — `body_hash`, computed at install-or-check time over the file body excluding the stamp line — and use the pair to distinguish four cases on every install:

|  | embed unchanged (`embed_hash == stamp`) | embed changed |
|---|---|---|
| **user has not edited** (`body_hash == stamp_at_install`) | skip | update |
| **user has edited** (`body_hash != stamp_at_install`) | skip | **conflict — preserve user, write embed as `<name>.dist`** |

`--force` keeps its name but changes its behaviour: when the user has edited, it backs the current file up to `<name>.bak.<timestamp>` *before* overwriting, and prints the backup path. Silent stomping disappears entirely. To get the old behaviour (no backup, no warning) the user passes `--force --no-backup`, which is loud enough in `--help` to be clearly opt-in.

`body_hash` is the hash of the embed payload that was written, recorded into the stamp at install time alongside `embed_hash`. New stamp shape:

```
<!-- pogo-prompt: embed=sha256:abc… body=sha256:def… -->
```

The pre-existing single-hash stamp is read for backwards compatibility (treated as both fields equal) so users on v0 stamps don't all hit "user has edited" on the v1 upgrade.

### File-class boundary

Three classes, made explicit in code so `--force` semantics stop being a trap:

- **Shipped templates** — `mayor.md`, `pm/pm-template.md`, `crew/doctor.md`, `templates/polecat.md`, `templates/polecat-qa.md`, plus anything future. Stamp + drop-ins + conflict detection all apply.
- **Shipped configs** — `pm/*.toml`. Same gating today; design folds them into the drop-ins / conflict-detection model identically. (Optional, deferrable: shipped configs could be moved to `pm/dist/*.toml` with the user copy at `pm/*.toml` always treated as drop-in. Cleaner long-term, but a one-time migration; flag for Daniel.)
- **User-owned** — `crew/<name>.md` (user-created), `pm/<instance>/memory/`, `pm/<instance>/sweep.log`, `dropins/`. Install never reads, writes, or warns about these.

### `pogo install --force` semantics, restated

After this design lands:
1. By default, `pogo install` skips files whose stamp matches the embed (no work to do); updates files where the embed has changed *only if the user has not edited the canonical file*; and on user-edited files, writes the new embed alongside as `<name>.dist` and tells the user to reconcile.
2. `pogo install --force` overwrites everything — but always backs up user-edited files to `<name>.bak.<ISO-8601>` first, unless `--no-backup` is also passed.
3. `dropins/` is never touched by either mode.
4. Live state (`memory/`, `sweep.log`, anything not in the embed) is never touched, as today.

## Migration

For users who already have customisations stomped by past `--force` runs: nothing in this design recovers them retroactively. The doc should call out backup hygiene (recommend `git init ~/.pogo/agents/` for users who customise heavily; pogo could ship an opt-in `pogo agent backup` command in a follow-up). Going forward, the new gating + drop-ins make the loss case rare and the surviving cases recoverable from `<name>.bak.*`.

For shipped-config flux (e.g., a future pogo version adds a new field to `pm-template.md`): users on the drop-in path are unaffected; users on the canonical-edit path get a `.dist` and a clear warning. Either way, no silent data loss.

For new agents added in pogo updates (a fresh `crew/<role>.md` ships in vNEXT): install creates them on first run. No migration question — there is no prior user file to conflict with.

## Roadmap (follow-up implementation tickets)

Filed once Daniel picks. Sized in rough working days.

1. Drop-ins loader: scan `dropins/<base>/*.md`, lexical-order append, wire through `SynthesizeExtendsPrompt` and `ExpandTemplate` — 1–2d.
2. TOML drop-in merge for `pm/<instance>.toml` — 1d.
3. Stamp v1 (`embed=…  body=…`), backwards-compat read of v0 — 0.5d.
4. Conflict detection: write `.dist` on user-edited+embed-changed; warn — 1d.
5. `--force` backup-on-overwrite, `--no-backup` flag, help text — 0.5d.
6. `pogo agent prompt show <name>` (or equivalent) prints synthesised result — 0.5d.
7. Optional: move shipped `pm/*.toml` to `pm/dist/*.toml` and treat user copy as primary — 1–2d (only if Daniel picks the cleaner long-term layout).
8. Docs: `docs/prompt-customization.md` (operator-facing, not this design doc) explaining drop-ins, dist files, and backup hygiene — 1d.
9. Tests: install→edit→update matrix across all four conflict cases, plus `--force` with and without `--no-backup` — 1d.

## Out of scope

- Three-way merge (debconf-style interactive resolution). Possible future enhancement on top of the `.dist` mechanism, but the conflict-detection design already gives the user everything they need to resolve manually.
- Customisation of non-prompt files (`~/.zshrc`, `~/.claude/CLAUDE.md`). User-side already.
- Versioned prompt repositories / prompt registries. Different problem.
- Full backup tool (`pogo agent backup`). Mentioned in migration as a possible follow-up; not specified here.

## Design rationale

Three things drove the shape:

1. **Reuse the stamp, don't replace it.** The hash-stamp infrastructure already exists; it just records the wrong thing. Adding a second hash and changing the comparison logic costs ~50 lines and makes silent stomping detectable. Inventing a new mechanism (sidecar metadata files, registry, etc.) costs more and protects no better.
2. **Drop-ins for the 80%, safety net for the 20%.** Most user customisations are additive ("add my house rules to mayor"). Drop-in dirs handle that case cleanly, in a convention that matches systemd / cron.d / sudoers.d that Unix-shaped users already know. Replacement edits — rarer, but real — are not forced through the drop-in convention; the conflict detector keeps them safe whether the user reads the docs or not.
3. **Make `--force` honest.** Today `--force` is the documented escape hatch but the *undocumented* behaviour is "every install eventually stomps user edits when the embed changes." Splitting out `--force` (loud, requires opt-in to skip backup) from the default (silent only when nothing user-touched) lines the surface up with what users probably already think it does.
