# Polecat worktree-confusion: post-mg-14c1 investigation

mg-1986 follow-up to mg-14c1 (commit `efef46c`).

## Method

Surveyed all 229 polecat session transcripts under
`~/.claude/projects/-Users-daniel--pogo-polecats-*/*.jsonl`. Flagged any
session whose Bash tool calls included `cd /Users/daniel/dev/pogo` or
`cd /Users/daniel/dev/pogo-reminders` — i.e. the polecat left its worktree
and operated against the canonical source repo. All 229 sessions are
post-fix (oldest mtime is 2026-03-26), so every incident below is post-fix.

## Findings

- 229 sessions surveyed, 34 (15%) had at least one `cd <canonical-repo>` Bash
  call.
- Of those 34: 17 destructive (state-mutating: `stash`, `pull`, `checkout`,
  `commit`, `go test`, `go install`), 17 read-only (`git log`, `git show`,
  `ls`).
- Worktree process `cwd` was always correct (every event in every transcript
  carried `cwd=/Users/daniel/.pogo/polecats/<id>`). Hypothesis 3 — "process
  spawned with wrong cwd" — is **not** the cause. `internal/agent/api.go`
  sets `Dir: worktreeDir` on the SpawnRequest (line 667), and
  `internal/agent/agent.go:267` honors it.

## Root causes (ranked by frequency)

### 1. Polecats don't realize their worktree shares git history with the canonical repo

**Frequency:** ~22 of 34 incidents (~5 destructive + ~17 read-only).

The dominant pattern: the polecat wants to inspect or compare against
`main`, doesn't know that `git log main`, `git diff main..HEAD`, and
`git show main:path/to/file` all work from inside the worktree (worktrees
share `.git` infrastructure with the source checkout), and `cd`s to the
canonical repo as a stand-in for "main".

Concrete examples (post-fix):

- `73a5` (2026-04-30) — *"Check if test failure is pre-existing on main"*:
  ``cd /Users/daniel/dev/pogo && git stash list && go test -run TestFileInNewProjectAddsProject ./internal/project/``.
  Then realized the risk and fell back to
  ``cd /tmp && git clone --depth 1 /Users/daniel/dev/pogo pogo-main-test && go test ...``.
- `486f` (2026-04-30) — *"Check if test fails on main"*:
  ``cd /Users/daniel/dev/pogo && git stash && go test ... ; git stash pop``
  — actively stashed user state in the canonical checkout to "test against
  main".
- `df4a` (2026-04-28) — *"Verify worktree matches main"*:
  ``cd /Users/daniel/dev/pogo && diff /Users/daniel/.pogo/polecats/df4a/internal/service/service.go ...``
  (could have been `git diff main -- internal/service/service.go` from the
  worktree).
- `pc-156b`, `pc-913e`, `pc-11d3`, `pc-6805`, `pc-4fa7`, `pc-214a`,
  `pc-d301`, `pc-e463`, `pc-f6f8`, `df4a-r` — all read-only `cd /Users/daniel/dev/pogo && git log ...`
  for context-gathering, where `git log main` from the worktree would have
  worked.

The polecat template's anti-cd warning is currently buried inside step 3
of the protocol ("Run all commands in this directory — do not `cd` to the
source repository"), with no explanation of why or what the alternative is
("look at main? use `git log main` from here"). The fix from mg-14c1
templated `{{.WorktreeDir}}` into the prompt but did not address this
specific failure mode.

### 2. `**Repository:** {{.Repo}}` at the top of the prompt is a strong attractor

**Frequency:** Underlies cause #1, but worth calling out separately.

The post-mg-14c1 polecat template renders:

```
**Repository:** /Users/daniel/dev/pogo

**Working Directory:** /Users/daniel/.pogo/polecats/1986
```

These two adjacent fields look symmetrical, with no signal that one is a
forbidden directory and the other is the only legitimate cwd. The
`Repository:` value is also passed to `pogo refinery submit --repo=...` in
step 5, so the polecat *needs* to know it — but as a label/argument, not as
a directory to enter. Relabeling the field to something like
`**Source repo (do not cd here — used only for `--repo` argument):**`
would weaken the attraction.

### 3. polecat-qa.md never received the mg-14c1 anti-cd warning

**Frequency:** Hard to count (QA polecats are rarer in the corpus), but
this is a code-review finding rather than a transcript finding.

`internal/agent/prompts/templates/polecat-qa.md` got the `**Working
Directory:** {{.WorktreeDir}}` field added in mg-14c1, but did NOT receive
the corresponding "Run all commands in this directory — do not `cd` to the
source repository" instruction that polecat.md got in step 3. So QA
polecats are still using the pre-mg-14c1 phrasing.

## Out of scope (separate problems)

These showed up in the transcript survey but are not what mg-1986 is
about. Documenting so they don't get conflated:

- **Cross-repo investigation** (`r8419`, `pc-a96b`, `pc-8419`, `pc-f44f`,
  `pc-123c`): polecat assigned to repo A (`pogo`) reaches into repo B
  (`pogo-reminders`) for legitimate cross-repo research, but operates on
  B's canonical checkout because there is no cross-repo worktree story.
  Fundamentally different problem: the polecat sometimes *needs* to see
  another repo, and we have no isolation story for that.
- **Install-style polecats** (`install`, `9cdc-r`, `9cdc`, `mg-9cdc`):
  these polecats' job *is* to update the canonical pogo install
  (`git pull origin main && go install ./...`). By design, not a bug. But
  fragile: the canonical repo can have uncommitted user changes when these
  fire.
- **Recovering from accidental writes to canonical repo** (`pc-ddc1`,
  `pc-39ab`): both ran `cd /Users/daniel/dev/pogo && git checkout
  cmd/{pogo,pogod}/main.go && git status` — looks like they were
  *reverting* changes they had mistakenly applied to the canonical repo.
  Symptom of cause #1, but the recovery itself isn't fixable from the
  prompt.

## Recommended fix

Two-part prompt template change in
`internal/agent/prompts/templates/polecat.md` and `polecat-qa.md`:

1. **Replace** `**Repository:** {{.Repo}}` with a label that signals
   "argument value, not directory":

   ```
   **Source repo (do not cd here — argument for `--repo` only):** {{.Repo}}
   ```

2. **Add a "Working in your worktree" section** right after the
   Working Directory line, BEFORE the Protocol section, explaining:
   - Your worktree at `{{.WorktreeDir}}` shares git history with the source
     repo. `git log main`, `git diff main..HEAD`, `git show main:path`,
     `git checkout main -- path` all work from here.
   - Never `cd {{.Repo}}`. The source repo may have uncommitted user
     changes; running `git stash`, `go test`, `git pull`, or `git checkout`
     there can corrupt user state.
   - If you need to run a command "against main", run it from your
     worktree using a `main`-relative ref, not by changing directory.

3. **polecat-qa.md** — same change (also currently missing the step-3
   "do not cd to the source repository" sentence from mg-14c1).

This is a small, conservative change confined to the two prompt template
files. It does not address the out-of-scope cases above (cross-repo and
install-style polecats) — those are separate tickets if Daniel wants to
follow up.

## Confidence

The dominant cause (cause #1) is well-supported by the transcript
evidence: every destructive incident's tool description ("Check if test
fails on main", "Verify worktree matches main", etc.) matches the
"compare against main" intent. The fix does not test against future
polecat behavior, so we should treat this as a hypothesis to validate by
re-running the survey in a couple of weeks after the prompt change ships.
