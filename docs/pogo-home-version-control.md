# `~/.pogo` is also a git working tree — what that repo is for

**Status: decided (mg-3610, 2026-08-06). The remedy in "Fixing this host" is a
machine-local ops action for Daniel; no agent performs it.**

On this fleet's host, `$POGO_HOME` (`/Users/daniel/.pogo`) is simultaneously two
things:

1. the live state directory of a running fleet — mail spool, `schedules.json`,
   `events.log`, per-agent runtime dirs, and the prompt files every crew agent
   and every polecat reads *as it runs*; and
2. the working tree of `https://github.com/drellem2/pogo-config.git`, a
   **private** GitHub repo that tracks 16 files in that directory.

That visibility is measured, not assumed — 2026-08-13, `gh repo view
drellem2/pogo-config --json isPrivate,visibility` →
`{"isPrivate":true,"visibility":"PRIVATE"}`, and `git ls-files` in `~/.pogo`
returns 16 paths. **This document said *public* from its decision (mg-3610,
2026-08-06) until mg-ee70 corrected it**, which is worth knowing because
visibility is the single property that governs what may safely be committed
there, and the tracked set includes `agents/crew/pa.md` — Daniel's email
address, his calendar ids, and a full description of his personal-assistant
setup. The error was in the map rather than the territory — that tracked set is
not world-readable. Note the tense: `isPrivate` is a present-tense measurement,
and GitHub stops showing a repo's public events once it is private, so nothing
here establishes what the visibility was between creation (2026-07-07) and now.
Re-measure rather than trusting this line — a repo's visibility is one `gh repo
edit` away from changing under a document.

Nothing in the pogo repo recorded that dual nature — before mg-3610 the string
`pogo-config` appeared nowhere in it — and nothing in pogo-config says what it
is a config *for*. It was deducible from neither repo alone, which is how
several agents spent 2026-08-05 reasoning about prompt staleness against a
baseline they had not identified. This file and `internal/homevcs` are what
closed the pogo-repo half of that gap.

`pogo doctor --check` now carries a `$POGO_HOME version control` row that
reports this condition on any host (see `internal/homevcs`, `cmd/pogo/homevcsdrift.go`).

## The decision

**pogo-config versions config that has no upstream. It does not record installed
state.**

The operational rule, which is what the doctor row checks:

> A file belongs in pogo-config if and only if **no pogo process writes it**.

Everything pogo writes under `$POGO_HOME` already has an authoritative answer
somewhere else:

| Path | Written by | Source of truth | Tracked? |
|---|---|---|---|
| `agents/mayor.md`, `agents/crew/doctor.md`, `agents/templates/*.md`, `agents/pm/pm-template.md` | `agent.InstallPrompts`, from the binary's embed | `internal/agent/prompts` in the pogo repo | **no** |
| `projects.json` | pogod's project discovery | this machine's disk | **no** |
| `agents/crew/architect.md`, `pa.md`, `pm-dealdesk.md`, `pm-lineara.md`, `pm-onethird.md`, `pm-pogo.md` | humans and agents, by hand | this repo — nowhere else | **yes** |
| `bin/pogo-recovery.sh`, `bin/vix-*` | humans and agents, by hand | this repo — nowhere else | **yes** |
| everything else (mail, schedules, events.log, per-agent state, the pogo binary) | pogod | — | **no** (the `.gitignore` is an allowlist) |

### Why not the other coherent design

Making pogo-config the *deployment record* of what is installed is defensible in
the abstract, and it was rejected on three grounds:

- **It requires the installer to commit.** Otherwise the tree is dirty the
  instant `InstallPrompts` runs, which is the state we are fixing. An installer
  that commits turns one machine's install output into shared history, and the
  second host to install produces a conflict in every prompt file at once.
- **It answers a question that is already answered.** "Is the installed corpus
  what the repo ships?" is exactly what `pogo check-staleness` compares
  (mg-dd49), against pogo source, without a second repo existing. Two records of
  one fact is what produced three simultaneous generations of
  `agents/templates/polecat-build-pr.md` on 2026-08-05 — pogo-config `HEAD`
  (oldest), the working tree (middle), pogo `origin/main` (newest) — with no rule
  saying which was authoritative.
- **It puts an automatic committer in a live blast radius.** The directory holds
  the mail spool and `events.log` of a running fleet. Writing git history into
  it on a schedule buys nothing the first two points do not already provide.

With the decision applied, **"is the installed prompt current?" has exactly one
answer**: compare against pogo source (`pogo check-staleness`, and the
prompt-drift row of `pogo doctor --check` for the binary's own embed).

## The prohibition

**Do not resolve the dirty tree by committing it.** Measured 2026-08-06, `git
status` in `~/.pogo` shows 11 modified files, 431 insertions, 162 deletions, and
that diff is a *mixture*:

| | insertions | deletions | what it is |
|---|---|---|---|
| `mayor.md`, `crew/doctor.md`, `templates/*.md` (5), `projects.json` | 406 | 152 | **pogo output.** Never commit. |
| `crew/architect.md`, `crew/pa.md`, `crew/pm-onethird.md` | 25 | 10 | **real edits with no upstream.** Commit these — see below. |

Committing the whole 431 records one machine's install output as shared truth,
re-dirties on the next install, and still does not say which generation of any
file is authoritative. It also buries the 25 lines that matter.

Those 25 lines are the more urgent half in one respect: the **only** copy of
those three crew-prompt edits is an uncommitted working-tree file inside a
directory that a stray `git checkout`/`git stash` would erase. They are the work
pogo-config exists to protect, and the install output is what has been hiding
them.

## The hazard, stated precisely

`~/.pogo` sits at pogo-config `ec68dc1` while `origin/main` is one commit ahead
at `1b7d1e7` (`mg-6a2a`, the VIX report), carrying 431 uncommitted lines.

Measured 2026-08-06: **that specific pending commit touches only `.gitignore`
and `bin/vix-*`, so a `git pull` today would fast-forward cleanly** and would not
conflict. The hazard is structural, not imminent — it fires the moment any
commit into pogo-config touches a tracked prompt file, which is precisely what
crew-prompt work does. When it fires, the conflict lands in the files the crew
is reading *while it is reading them*, and resolving it in either direction
silently reverts either the installed prompts or the committed crew-prompt work.

Until this host is reconciled:

- **Do not** `git pull`, `git checkout`, `git reset --hard` or `git stash` in
  `~/.pogo`. `git status` being dirty there is the normal state, not a mess to
  clean up.
- **Do not** dispatch a polecat with `--repo=/Users/daniel/.pogo`.
- Anything *added* to `~/.pogo` — a script, a config, a note — may be committed
  and pushed to GitHub. The repo is private, so that is off-host rather than
  world-readable; it is still off-host, and a later `gh repo edit --visibility
  public` would expose whatever was committed under today's reading. The
  allowlist `.gitignore` is what stops an addition by default; new top-level
  paths need an explicit un-ignore, and reviewing `git ls-files` before any
  commit there is the check that keeps a secret off GitHub.

## Fixing this host (ops, for Daniel)

The order matters, and none of it happens in the live tree except the last two
steps. `git rm --cached` and `git reset --mixed` are the two operations that
change git's mind without touching a file on disk — that property is why this
sequence is safe and why `pull`/`checkout` are not.

1. **In a scratch clone, not in `~/.pogo`.** Clone pogo-config somewhere
   disposable and work there:

   ```bash
   git clone https://github.com/drellem2/pogo-config.git /tmp/pogo-config
   cd /tmp/pogo-config       # already at origin/main
   ```

2. **Untrack the install- and daemon-written paths, and stop allow-listing
   them.** pogo-config's `.gitignore` is an allowlist (`/*` then un-ignore), so
   the edit is to *remove* un-ignore lines, not to add ignores. The
   `agents/crew/` stanza keeps its wildcard and re-ignores the one crew prompt
   that ships from the pogo embed:

   ```gitignore
   # --- agents/: only prompts with NO upstream. Anything InstallPrompts
   # writes is owned by the pogo repo (internal/agent/prompts) and must not be
   # tracked here — see docs/pogo-home-version-control.md in the pogo repo.
   !/agents/
   /agents/*
   # crew prompts that live nowhere else...
   !/agents/crew/
   /agents/crew/*
   !/agents/crew/*.md
   # ...except doctor.md, which ships from the pogo embed
   /agents/crew/doctor.md
   # never track editor lock symlinks
   /agents/crew/.#*
   ```

   and delete the `!/agents/mayor.md`, `!/projects.json`, and
   `!/agents/templates/` blocks outright.

   ```bash
   git rm --cached -r agents/templates
   git rm --cached agents/mayor.md agents/crew/doctor.md projects.json
   git commit -m "pogo-config versions config with no upstream; pogo's own output is not tracked (mg-3610)"
   git push origin main
   ```

3. **Move the live tree's pointer without touching the live files.**

   ```bash
   cd ~/.pogo
   git fetch origin                 # fetch only. never pull.
   git reset --mixed origin/main    # moves HEAD + index; writes no working file
   ```

   After this the install output is untracked-and-ignored, still byte-for-byte
   what the installer wrote, and the tree stops being permanently dirty.

   `--mixed` discards no history here: measured 2026-08-06, `git rev-list
   --count origin/main..HEAD` in `~/.pogo` is **0** — the live tree has no
   local-only commits, it is simply one commit behind. Re-check that count
   before running the reset; if it is not 0, someone committed in the live tree
   and those commits need pushing first.

4. **Commit the 25 lines that are real.** `git status` should now show only the
   hand-authored crew prompts. Read the diff before committing — this is the
   step where genuine prompt work gets preserved:

   ```bash
   git diff agents/crew/
   git add agents/crew/architect.md agents/crew/pa.md agents/crew/pm-onethird.md
   git commit -m "crew prompt edits made on the host"
   ```

5. **Verify.** `pogo doctor --check` should move the `$POGO_HOME version
   control` row from `warn` to `pass`, naming the repo and reporting that it
   tracks none of the paths pogo writes. If a *new* embedded prompt is added to
   the pogo repo under `agents/crew/` later, that row is what will catch the
   wildcard picking it up.

Nothing in steps 1–4 requires stopping the fleet, and no step rewrites a prompt
file the crew is reading.

## See also

- `internal/homevcs` — the audit; read-only by construction, including
  `--no-optional-locks` so it never takes `index.lock` in a live home.
- [`docs/operations.md`](operations.md) — the operator-facing summary of this row.
- `pogo check-staleness` — the one comparison that answers "is the installed
  prompt current?" (mg-dd49).
- mg-6a2a — the ticket that put a script in `~/.pogo` on the belief that the
  directory was untracked local state. It was not; the belief was reasonable,
  which is the point.
