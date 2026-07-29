# The merged `resolve_git` runner is now the one launchd executes — proved, not assumed

**Date:** 2026-07-29
**Work item:** mg-bcc1 (the mg-36e3 remainder no item owned)
**Verdict:** The cutover is **done and proved** for everything observable at install
time. One sub-step — the next *real* 02:00–05:00 window run — is **not yet observed**
and is recorded below as an open follow-up, not as a pass.
**Xref:** mg-b72a (`2123fdc`, the `resolve_git` change), mg-42ac (the
`com.pogo.deploy` LaunchAgent), drellem2/pogo#93

## Why this needed its own item

`~/Library/LaunchAgents/com.pogo.deploy.plist` runs
`/Users/daniel/.pogo/bin/pogo-deploy.sh` — an installed **copy**, not the file in the
repo. Merging `scripts/launchd/pogo-deploy.sh` therefore changes nothing about what
fires at 03:00. Before this item, the installed copy was byte-identical to the
*pre-merge* runner and would have kept running indefinitely.

This is the merged-but-not-live class that mg-b7d0/mg-42ac exist to end, and it is
invisible precisely because every repo-side check passes.

## The before state (measured, not recalled)

    sha256(~/.pogo/bin/pogo-deploy.sh)          = 6b56fd25...d89e7db9
    sha256(git show 2123fdc^:scripts/.../…sh)   = 6b56fd25...d89e7db9   ← identical

The installed copy *was* the pre-merge runner: `GIT="${GIT:-/usr/bin/git}"` at :91,
no `resolve_git`, and **zero** occurrences of the string `resolved working git`.

## What was run

    pogo service install-deploy

with cwd at a checkout of `main`. Two notes on why that is the faithful operator
action rather than a shortcut:

- `findDeployScriptSource` resolves `<cwd>/scripts/launchd/pogo-deploy.sh` after the
  binary-relative candidates miss, so the script installed is main's.
- The `pogo` on `PATH` is built from `023fab5`, which is an ancestor of main whose
  `internal/service/deploy.go` is **identical** to main's. The plist it renders is
  therefore exactly the plist main defines — the stale binary cannot skew the result.

## The four gates

**1. The installed artifact actually changed.**

    sha256(~/.pogo/bin/pogo-deploy.sh) = 094d3c9f...5df87d85
    sha256(scripts/launchd/pogo-deploy.sh) = 094d3c9f...5df87d85   ← identical to main

`git_candidates()` at :211 and `resolve_git()` at :220 are present; `GIT=` is now
`GIT="${GIT:-}"` at :117; the `GIT="${GIT:-/usr/bin/git}"` pin is gone; mode is 0755.

**2. No `POGO_DEPLOY_ZSHENV` key in the plist.**

Stronger than a grep: `InstallDeploy` writes the plist **only when the rendered bytes
differ** from what is on disk (`deploy.go:231`). The file's mtime is unchanged at
Jul 28 23:13, which means main's template rendered byte-for-byte what was already
installed. The dropped half did not reappear because there was nothing to reappear.
`launchctl print` confirms the live environment is `PATH`, `HOME`, `POGO_HOME`,
`POGO_DEPLOY_SRC` and nothing else.

**3. launchd's live view points at the replaced file.**

    program = /Users/daniel/.pogo/bin/pogo-deploy.sh
    event triggers = { calendarinterval: Hour => 3, Minute => 0, watching = 1 }
    RunAtLoad = false

Installing did not bounce anything (`active count = 0`, `state = not running`). The
`runs = 0` counter is reset by the `bootout`/`bootstrap` pair `InstallDeploy` always
performs, not by a missed fire.

**4. End-to-end: the new runner is the one that executes.**

    POGO_DEPLOY_SKIP_WINDOW=1 ~/.pogo/bin/pogo-deploy.sh --dry-run

run under `env -i` with exactly the plist's four environment variables (launchd does
not source `~/.zshenv`, so replicating only the plist env is the honest reproduction).
The window guard needed the documented `POGO_DEPLOY_SKIP_WINDOW=1` bypass because
19:00 local is outside `[2,5)` — without it the run exits at gate 1, *before*
`resolve_git`, and would have proved nothing. Output:

    git: resolved working git at /usr/bin/git (git version 2.50.1 (Apple Git-155))
    sync: /Users/daniel/.pogo/deploy-src at main 2123fdc
    dry-run: would run '.../pogo-self-deploy redeploy --yes' (never --force). Stopping here.

Exit 0, reproduced twice.

**Why that line is proof and not decoration.** The string `resolved working git` does
not exist anywhere in the pre-merge runner — the negative control above counted zero
occurrences in the file whose sha256 was installed until today. It is not a line the
old script could emit under any environment. Its appearance is therefore positive
identification of *which file ran*, which is the only thing step 4 was ever asking.

**What it deliberately does not show.** `resolve_git` selects `/usr/bin/git` here,
the same binary as before, because neither `/opt/homebrew/bin/git` nor
`/usr/local/bin/git` exists on this host. There is **no behavioural change to
observe**, and a verification that passed because nothing changed would prove nothing.
That is why the identification is done by a string unique to the new file rather than
by a difference in outcome.

## Open follow-up — the real fire

The last half of step 4, "the next real 02:00–05:00 window run completes green," is
**not yet observed**. The next scheduled fire is 03:00 local on 2026-07-30 (02:00Z),
and it cannot be brought forward without either a real redeploy (`launchctl kickstart`
runs the job with no `--dry-run`) or a bypass that would make it something other than
the real fire.

To confirm it, read `~/Library/Logs/pogo/pogo-deploy.log` after 02:00Z and check for:

- `window: local hour 03 is inside [2,5)` — the real gate, not the bypass;
- `git: resolved working git at /usr/bin/git (git version ...)`;
- a terminal `pogo-deploy: done — pogod redeployed to <sha>` at or after `2123fdc`.

The 2026-07-29 02:00Z run in that log is the last *pre-cutover* fire; it deployed
`023fab5` and shows no `git:` line at all. That absence is the baseline the next entry
should be read against.

## Note for whoever reads the log

The manual dry-run above was appended to the production log behind a
`---- MANUAL CONTROLLED DRY-RUN (mg-bcc1 verification, not a nightly fire) ----`
marker, so it is not mistaken for a 03:00 fire that arrived sixteen hours early.

The dry run did advance `~/.pogo/deploy-src` from `023fab5` to `2123fdc` via
`sync_src`, which is fetch + refuse-if-dirty + `--ff-only` and nothing else. That is
the same advance tonight's fire would have made; nothing was built, installed, or
restarted, and the running pogod is untouched at `023fab5`.
