# Contributing to Pogo

Thanks for your interest in contributing to pogo! This guide covers the basics of building, testing, and submitting changes.

## Getting Started

1. Fork and clone the repository
2. Install Go (1.25+)
3. Run `./build.sh` to build and test

## Development Workflow

### Building

```bash
./build.sh              # Format, test, and build all binaries into ./bin
./build.sh --install    # ...and also `go install` them into GOBIN
./test.sh               # Run tests only
./fmt.sh                # Format code only
```

`./build.sh` runs all three steps (format, test, build) and is the recommended way to verify your changes before committing.

Binaries land in `./bin` (gitignored). `./build.sh` deliberately does *not* write
to GOBIN: it runs unattended in agent worktrees and as the refinery's quality
gate, where a `go install` would overwrite the machine's installed `pogod` with
an unreviewed branch build. Pass `--install` when you actually want the binaries
on your `PATH`, or set `POGO_BUILD_DIR` to redirect the output directory.

### Writing a test that touches pogo state: use `scripts/pogo-sandbox`

A test must never read or write the developer's live `~/.pogo`, the live daemon,
or the live fleet. Four separate tickets were filed for that one defect
(`mg-6092`, `mg-e8e7`, `mg-5336`, `mg-3412`) because every suite re-derived its
isolation by hand, and the fourth gates every merge: under fleet load it drove
another run's daemon and reported **fourteen assertion failures**, including
verbatim fail-open findings, about a tree that was provably fine.

So do not hand-roll the overrides. Source the packaged harness:

```bash
source "$REPO_ROOT/scripts/pogo-sandbox"
pogo_sandbox_create mytest        # a private root; no env change yet
trap pogo_sandbox_down EXIT
# ...build anything that needs the real HOME (Go resolves GOMODCACHE off it)...
pogo_sandbox_isolate              # HOME/XDG_CONFIG_HOME/POGO_HOME/MG_ROOT pinned AND checked
pogo_sandbox_daemon "$POGO_SANDBOX_DIR/pogod" /scheduler/schedules
pogo_sandbox_curl "register the mail-check" -- \
    -X POST "$POGO_SANDBOX_URL/scheduler/schedules" -H 'Content-Type: application/json' \
    -d "{\"id\":\"mail-check-$(pogo_sandbox_name pa)\",\"agent\":\"$(pogo_sandbox_name pa)\",...}"
```

For a suite that is not written in shell, the whole command can be wrapped:

```bash
scripts/pogo-sandbox run -- go test ./internal/agent/...
scripts/pogo-sandbox run --daemon ./bin/pogod -- ./my_test.sh
```

What it guarantees, and *checks* rather than assumes:

- **A private home that cannot be the developer's.** All four of `HOME`,
  `XDG_CONFIG_HOME`, `POGO_HOME` and `MG_ROOT` are pinned and then resolved
  through symlinks; any one landing on, above, or below the real `~/.pogo` is
  refused. All four are needed: this box exports `POGO_HOME=$HOME` from a stale
  profile, so setting `HOME` alone still writes the live tree.
- **A private port**, reserved atomically and proven to belong to the process
  this run started — never probed for.
- **Names no live fleet holds.** `pogo_sandbox_name` mints them, and
  `pogo_sandbox_curl` *refuses* a write whose schedule or agent identity does not
  carry the run's token — so re-introducing a write to `mail-check-pa` is a
  failure, not a habit lapse.
- **A setup failure that cannot be read as a regression.** Any of the above ends
  the run with a `SETUP FAILURE` banner and **exit 99**, distinct from the
  assertion tally's 1, before a single assertion runs.

Never weaken an assertion to make it fit the harness. If conversion appears to
need that, the assertion is telling you something — stop and ask.

Three things that only show up when a suite is converted (`mg-b4a5`):

- **"Build before `isolate`" does not cover a build in a CHILD.** The rule exists
  because Go resolves `GOMODCACHE`/`GOCACHE`/`GOPATH` off `$HOME`, so a build
  after the override re-downloads the whole module cache into the sandbox. A
  control that *spawns* something which builds cannot obey it by ordering. Read
  the three paths under the real `HOME` and export them before `pogo_sandbox_isolate`
  instead. They are build caches, not pogo state: none of the four pinned
  variables is reachable through them.
- **Do not let `POGO_SANDBOX_ROOT` reach a child that makes its own sandbox.**
  `pogo_sandbox_create` honours it, so an exported one puts parent and child in
  one directory — where the child's `isolate` finds a non-empty `POGO_HOME` and
  its teardown `rm -rf`s the root the parent is still using. `unset` it once
  yours has been created.
- **Converting a control that breaks the harness on purpose: only the
  scaffolding goes inside the envelope.** The break itself must stay in its own
  subshell or process, because the refusal under test *is* `exit 99` and
  in-process it would end the file at that assertion. `scripts/pogo-self-deploy_live_setup_test.sh`
  is the worked example: its root, its fake binaries and its `HOME` are the
  harness's; its three deliberate breaks are not, and it never reserves a port or
  starts a daemon of its own, so nothing its own setup depends on is a thing it
  deliberately breaks.

### Writing a Go test that touches pogo state: use `internal/testsandbox`

`scripts/pogo-sandbox run --` is the cheap whole-command version and it works
today. `internal/testsandbox` is the per-test version, and it is what a Go suite
should be written against — the same four variables, the same symlink
resolution, the same refusal, raised through `t.Fatal` and `os.Exit(99)` instead
of a shell banner (`mg-0941`).

```go
func TestMain(m *testing.M) {
    sb, down := testsandbox.Main("mypkg")  // pinned AND proven, or exit 99
    code := m.Run()
    down()
    os.Exit(code)
}

func TestSomething(t *testing.T) {
    home := testsandbox.Isolate(t).Home    // a tree of this test's own
}
```

Do **not** hand-roll `t.Setenv("HOME", t.TempDir())`. It is correct Go and it is
still isolation-by-remembering — nothing checks that the override took, that the
other three variables were pinned alongside it, or that the value does not
resolve back onto the developer's tree. All three of `mg-6092`, `mg-e8e7` and
`mg-5336` were hand-rolled fixes that left the next instance available.

Two rules that fall out of it:

- **`POGO_HOME` is always set under the sandbox.** `config.PogoHome()` reads it
  *first* and only falls back to `$HOME/.pogo`, so a test that repoints `HOME`
  alone no longer moves the state root with it. If a test needs a synthetic home
  (`internal/project`'s `TestIsEphemeralPath` does), pin `POGO_HOME` next to it.
- **Give each package a positive control.** One test that calls
  `testsandbox.Verify(t, sandbox)` and then asserts that the path the package
  actually writes through — `config.PogoHome()`, `resolvePluginPath()`,
  `ParkFilePath(...)` — satisfies `sandbox.Contains`. Without it the isolation is
  an unverified claim, and a later edit can drop it with every other test in the
  package still green. That is exactly how all three tickets shipped.

### The isolation is enforced, not suggested (`mg-457b`)

The two sections above used to be advice, and advice is opt-in. On 2026-07-29 the
tree held **56 test suites and 8 adopters**, and **nothing failed** for the other
48 — a suite written next month read the developer's live state unless its author
recalled a helper they may never have seen. All four measured instances of the
defect (`mg-6092`, `mg-e8e7`, `mg-5336`, `mg-3412`) are authors who did not set
out to read live state at all.

So `TestEveryTestSuiteRoutesThroughTheIsolation`, in
`internal/testsandbox/adoption_test.go`, walks the tree on every `go test ./...`
and fails when a suite neither adopts nor is named in
`internal/testsandbox/adoption-ledger.txt`. It is a **test** rather than a CI step
on purpose: it fails locally, before the merge queue, where the author of the new
suite can still act on it.

The unit of adoption is the **test binary** — a Go package is one entry however
many `_test.go` files it holds, and a shell suite is one file.

**The ledger is a ratchet, not an allowlist.** The same check fails when a
ledgered suite has since adopted (delete the line) or no longer exists — so
converting a suite is a one-line deletion, and the list can only get shorter.
There is no "this one needs live state" marker, because no suite in this
repository has been found to need one; if you believe yours is the exception,
say so on a work item where somebody can read the argument.

If the check names your new suite, adopt the isolation. **Do not add a line.**

### Writing a test that drives the real `mg`: declare the clause (`mg-216c`)

`mg` lives in another repository and is not pinned. A test that asserts how it
behaves is a **cross-repo coupling that nothing announces**, and `./build.sh`
runs `go test ./...` while the refinery gate runs `./build.sh` — so one such
test takes down every pogo merge. On 2026-08-07 two *correct* macguffin changes
did exactly that ninety minutes apart, killing five branches that had nothing to
do with either.

Two questions, in order.

**1. Does this test need the real binary at all?** Keep it live only when the
cross-binary behaviour *is* what is under test — `internal/strandedmail`'s
`TestAgainstRealMg` earns it, because a mock keyed on our own struct tags could
never notice `mg` renaming an NDJSON field, which is that bug's exact shape. If
`mg` is merely building a fixture, use a stub: `internal/ghintake` is the worked
example — a shell case statement for the cases, one live control for the wire
format.

**2. If it stays live, say what it depends on.**

```go
func TestRefusedDoneKeepsTheResultItWasGiven(t *testing.T) {
    mgcontract.Require(t, mgcontract.DoneRefusalPreservesTheResultSidecar)
    ...
}
```

`internal/mgcontract` holds the declared contract — one named clause per
behaviour, with a probe, the `mg` work item that created it, and the pogo tests
resting on it. When a clause breaks, the dependent tests **skip** (their premise
is gone, so what they assert next is not a finding about pogo) and
`TestTheDeclaredMgContractStillHolds` fails **by name**, saying which behaviour
moved and what else to re-read. The gate is still red — pogo holds a stale
expectation and somebody must rule on it — but the occurrence costs a glance
instead of a full-suite hunt.

**Never resolve a break by editing the probe to agree with what `mg` now does.**
Decide which side is wrong first. A clause rewritten to match the dependency has
stopped testing anything, and every pogo test behind it is then resting on a
behaviour nobody checked. See [docs/design/mg-contract.md](docs/design/mg-contract.md).

### Code Style

- All Go code must be formatted with `gofmt`. The CI pipeline checks this.
- Run `./fmt.sh` or `gofmt -w .` to format your code.
- Follow standard Go conventions and idioms.

### Pre-commit Hook

Set up the pre-commit hook to catch formatting and build issues early:

```bash
git config core.hooksPath hooks
```

This runs `gofmt -l` and `go build ./...` on every commit.

The `commit-msg` hook additionally rejects commit messages whose closing
keywords would shut a GitHub issue — including across a line wrap, which is how
a narrative body once shut an external contributor's issue by accident. Cite
issues as `Refs owner/repo#N`, or add `Closing-ref-ack: <ref> — <why>` when the
closure is deliberate. The refinery runs the same check on every merge, so this
hook is an early warning rather than the guarantee.

### Writing a hook: it must self-activate from source

**A tracked gate must not depend on the installed `pogo`/`pogod`.** Tracked
files — hooks, prompts, templates — go live the instant a merge lands. The
compiled binaries go live only when self-deploy runs. Those are two different
clocks, and a gate that couples them is broken for the whole window in between.

This is not hypothetical: a commit-msg hook that called a subcommand from an
undeployed binary once rejected **every** commit in the repo, benign and
hazardous bodies alike, and it looked correct to whoever merged it. Identical
failure on both arms is the signature of a gate broken by its own dependency
rather than one catching bad input.

Two things are required, and the second is the one people miss:

1. **Have a source route** — `go run ./cmd/pogo ...`. Costs about a second warm.
2. **Guard on capability, not presence.** `command -v pogo` and
   `[ -x bin/pogo ]` only answer "does something named pogo exist". A stale
   binary satisfies both, wins the route, and leaves your source fallback
   unreachable in exactly the window it was written for. Ask the candidate
   whether it actually *has* the behaviour — run `<candidate> <subcommand>
   --help` as a separate, side-effect-free probe — so a stale binary falls
   through instead of winning.

`hooks/commit-msg` is the worked example. `internal/hookselfactivate` enforces
both rules over every tracked file under `hooks/` as part of `go test ./...`,
so a violation fails the build rather than waiting to be noticed. The check is
a Go test rather than a script calling `pogo`, so it is not subject to the
problem it detects.

Scope is `hooks/` only. `scripts/` legitimately builds and exercises sandbox
binaries as part of the self-deploy suite; a check that fired there would get
switched off, which is worse than one that fires narrowly.

## Submitting Changes

1. Create a feature branch from `main`
2. Make your changes in focused, atomic commits
3. Run `./build.sh` and ensure it passes
4. Open a pull request against `main`

### Pull Request Guidelines

- Keep PRs focused on a single change
- Include a clear description of what the PR does and why
- Ensure CI passes (formatting, build, tests)
- Commit messages should follow the format: `type: description`
  - Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`

## Project Structure

- `cmd/` - CLI entry points (`pogo`, `lsp`, `pose`, `pogod`)
- `internal/` - Internal packages
- `pkg/` - Public packages
- `emacs/`, `nvim/`, `vscode/` - Editor integrations
- `shell/` - Shell integrations (zsh, bash, fish)
- `tmux/` - tmux integration

## Releases

Releases are cut by tag-trigger: pushing a `vX.Y.Z` tag to `origin` triggers
`.github/workflows/release.yml`, which runs `goreleaser` and publishes the
GitHub release with all four binaries (`pogo`, `pogod`, `lsp`, `pose` for
linux/darwin × amd64/arm64). `install.sh`'s `releases/latest` resolver picks
up the new release within minutes.

**Tag-creation policy.** Only the release-cut path pushes `v*` tags — either
`scripts/bump-version.sh` (which validates strict semver and tags
annotated/signed) or a maintainer directly. No other automation creates tags.
Versioning is semver: **patch** for CI / docs / chore-only changes, **minor**
otherwise; reserve major for breaking CLI changes. Prereleases use a
`vX.Y.Z-<suffix>` form and surface as GitHub prereleases automatically.

**Cutting a release from a clean `main`.** With the change you want to ship:

```bash
./scripts/bump-version.sh X.Y.Z --commit --tag --push
```

This assembles the `changelog.d/` fragments into `CHANGELOG.md`, bumps
`internal/version/version.go`, rolls `CHANGELOG.md` (heading **and** compare link
references), commits, tags, pushes, and confirms the tag reached `origin`. The
release workflow does the rest.

**Cutting a release from any other branch — do NOT pass `--tag` (mg-cef7).**
`--tag` tags the local commit the script just made. Off `main` that commit does
not survive: the branch goes through the refinery, which **re-commits what it
merges** (v0.7.0's merged commit `4112875` carries committer *"pogo refinery"*),
so the tagged SHA is not the SHA on `main` and the tag dangles off a commit no
branch contains. `bump-version.sh` **refuses** `--tag` off `main` rather than
warning, because a pushed release tag is externally visible and force-pushing
does not unpublish it. Instead:

```bash
./scripts/bump-version.sh X.Y.Z --commit    # bump + changelog only, no tag
git push origin "$BRANCH"
pogo refinery submit "$BRANCH" --repo=<repo> --target=main
# ...then, AFTER the merge lands, on the MERGED sha:
git fetch origin main
git tag -a vX.Y.Z -m 'Release vX.Y.Z' origin/main
git push origin vX.Y.Z
git ls-remote --tags origin | grep vX.Y.Z      # a LOCAL tag proves nothing
```

**The post-merge tag needs an owner that outlives the merge.** Moving the tag
after the merge is the correct fix for the dangling tag, and it creates a second
problem: the merging worker cannot perform it. pogod stops a polecat on merge
success — measured at ~3s for the v0.8.0 cut — so any post-merge step the worker
still intends loses a race it does not know about. Both v0.8.0 cut attempts
(`mg-3685`, `mg-e084`) were correctly instructed, both merged, both were reaped
before tagging, and both work items read `done` with **no tag in existence**;
each was recovered only because a coordinator happened to run
`git ls-remote --tags origin` afterwards. So the tagging step belongs to a
coordinator or a separately-dispatched follow-up, never to the worker whose merge
closes the ticket — and the verification must query the **remote**, since neither
recovery had a local tag either. Automating this (refinery-side tag-on-merge, or
a follow-up ticket filed at bump time) is not yet done.

**Verifying the changelog's link references.** A version heading with no
`[X.Y.Z]:` link reference does not error — Markdown renders `[0.8.0]` as literal
text in the published changelog:

```bash
./scripts/changelog-links.sh
```

`bump-version.sh` runs this after rolling, so a cut that produced an unlinked
heading aborts. It compares the heading **set** against the link-reference set
and names the unmatched version and direction; it never reports a difference of
counts. That distinction is load-bearing — see the header of
`scripts/changelog-links.sh` for the measured case where the count formulation
fired correctly and diagnosed the wrong object, and the obvious remedy would have
entrenched the corruption it was reporting.

**Adding a changelog entry (per change, not per release).** Do **not** append to
`CHANGELOG.md` — write a fragment file instead: `changelog.d/<slug>.<category>.md`
(named by work-item id, e.g. `changelog.d/mg-1234.fixed.md`). Every change
appending to the shared `## [Unreleased]` tail collided there under concurrency,
and that one file was the dominant recorded merge-conflict cause (mg-d917); one
file per change makes the collision structurally impossible. See
`changelog.d/README.md` for the format. `bump-version.sh` folds the fragments in
at release time via `scripts/assemble-changelog.sh`, which **refuses to cut an
empty changelog** (no fragments and an empty `[Unreleased]` → non-zero exit).

**Checking that the rule was actually followed (mg-7904).** The empty-changelog
refusal above checks a *weaker* property than this section states. The rule is a
fragment **per change**; the guard only fires at **zero** entries. When that gap
was measured, 95 mg-ids had shipped in `feat:`/`fix:` commits since v0.5.0 and 51
had fragments — and the guard passed, because 51 is not zero. A cut would have
proceeded and shipped a changelog describing part of the release, silently.

`scripts/changelog-coverage.sh` checks the rule instead:

```bash
./scripts/changelog-coverage.sh                      # since the most recent tag
./scripts/changelog-coverage.sh --range v0.5.0..HEAD --json
```

It names its population — distinct mg-ids in `feat:`/`fix:` commit subjects in
the range — and reports, in separate buckets, how many are described by a
fragment, by a hand-written `[Unreleased]` entry, or not at all. It exits
non-zero when anything is undescribed, and `bump-version.sh` runs it before
assembling: an undescribed id **refuses the cut** unless you pass
`--ack-changelog-gaps`, which ships anyway and says so. The gap is reported to
whoever is *cutting* rather than whoever is *merging* — a per-commit gate at the
coverage this was written under would fail on work unrelated to the gap.

A passing guard is evidence about the property it checks, never about the rule it
was written to enforce. `scripts/changelog-coverage_test.sh` therefore leads with
a positive control: the check is shown to **fail** on a range with a known-missing
fragment before any passing case is trusted.

Two things the script does **not** do, which the releaser must:

- **Run the upgrade smoke first if the release changes a role-name default.**
  `./scripts/upgrade-smoke.sh` seeds a config from the previous release, upgrades
  to the working tree, and asserts that an existing install keeps its role names
  across both pin sites (`pogo install` and pogod boot) while a fresh install
  adopts the new ones. It is a **hard publish gate**: a red run means do not tag.
  The guard it protects is unrecoverable after the fact — an install whose role
  names were never pinned cannot have them recovered.
- **Maintain the link-reference block at the bottom of `CHANGELOG.md`.**
  `update_changelog()` only inserts the version heading; the `[X.Y.Z]:` compare
  links are hand-maintained. Each cut adds one line for the new version and
  repoints `[Unreleased]` at it:

  ```
  [Unreleased]: https://github.com/drellem2/pogo/compare/vX.Y.Z...HEAD
  [X.Y.Z]: https://github.com/drellem2/pogo/compare/vW.V.U...vX.Y.Z
  ```

  Miss it and the new heading renders as literal `[X.Y.Z]` text on GitHub, and
  `[Unreleased]` keeps claiming the commits you just released.

**Recovery from a failed publish.** If GitHub Actions is wedged or the
goreleaser step fails, the tag stays in place — re-trigger via
`workflow_dispatch` on the tagged ref once Actions recovers; goreleaser
handles idempotent re-uploads.

**Cadence.** `pm-pogo` files a `release-cut` `mg` ticket automatically once
origin/main drifts past either threshold (>= 50 commits ahead of the latest
release tag, OR >= 30 days since the latest published release). Thresholds
live in `internal/agent/prompts/pm/pm-template.md`.

## Reporting Issues

Use the GitHub issue templates for [bug reports](.github/ISSUE_TEMPLATE/bug_report.md) and [feature requests](.github/ISSUE_TEMPLATE/feature_request.md).

## License

By contributing, you agree that your contributions will be licensed under the [GPL-3.0 License](LICENSE).
