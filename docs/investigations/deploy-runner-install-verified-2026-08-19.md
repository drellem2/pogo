# The mg-9fc9 fleet-bounce runner is now the one launchd executes — hashes, not a copy that reported success

**Date:** 2026-08-19
**Work item:** mg-45b9
**Verdict:** **Done and proved** for everything observable at install time. The
one sub-step nobody can observe before it happens — tonight's real 03:00 fire —
is recorded below as an open follow-up, not as a pass.
**Xref:** mg-9fc9 (`980048f`, the fleet-bounce fallback), mg-3bb3 (`install-deploy`
overwrites the installed runner unconditionally — open, human-gated), mg-de0c,
`docs/investigations/deploy-runner-cutover-verified-2026-07-29.md` (the same class,
previous instance)

## Why this needed its own item

`~/Library/LaunchAgents/com.pogo.deploy.plist` runs
`/Users/daniel/.pogo/bin/pogo-deploy.sh` — an installed **copy**. Merging
`scripts/launchd/pogo-deploy.sh` therefore changes nothing about what fires at
03:00, and nothing in the nightly refreshes that copy.

mg-9fc9 landed at 11:27 today: after N consecutive nights lost at the transport
step, the nightly bounces the fleet without a remote. It exists because the
recovery path shared a dependency with the failure it recovers from. Left
uninstalled, that fix had the same shape one level in: **the remedy for "the
deploy cannot recover the fleet" was gated behind running a deploy** (pm-pogo's
framing, kept because it is the point).

## The before state (measured, not recalled)

    git hash-object scripts/launchd/pogo-deploy.sh   (main = 980048f)
      958fa229057b4e5546f65d0716f2d66072c0b249       211171 bytes
    git hash-object ~/.pogo/bin/pogo-deploy.sh
      aa5e3fe36882a766e030fd556fe5d5be2bad2d04       160146 bytes, mtime 2026-08-10 07:49

Different. **Three** main-ancestor revisions of the runner had landed since the
installed copy was current:

| commit | date | what the installed copy was missing |
|--------|------|-------------------------------------|
| `bdacc21` | 2026-08-12 | the network positive control |
| `7c2c6a0` | 2026-08-13 | fire hours read from the loaded plist |
| `980048f` | 2026-08-19 | **the fleet bounce (mg-9fc9)** |

### The installed copy carried no local modification — checked, not assumed

mg-3bb3 records that `install-deploy` overwrites this file unconditionally: no
drift check, no backup, no warning. Reproducing that here would have destroyed
any local edit silently, so the question was answered first:

    git rev-list --all --objects | grep '^aa5e3fe3'
      aa5e3fe36882a766e030fd556fe5d5be2bad2d04 scripts/launchd/pogo-deploy.sh

The installed bytes are a **reachable committed blob at that exact path** — a
pristine older repo copy, not a modified one. `fc0789c` (2026-08-11 21:49) is the
last main-ancestor commit whose tree carries it. Nothing was at risk, and a
backup was taken anyway:

    ~/.pogo/bin/pogo-deploy.sh.bak-mg-45b9-20260819T110052Z
      git hash-object → aa5e3fe36882a766e030fd556fe5d5be2bad2d04   (identical to what it replaced)

## What was run, and what was deliberately not run

    cp scripts/launchd/pogo-deploy.sh ~/.pogo/bin/pogo-deploy.sh && chmod 0755
    cp scripts/lib/net-control.sh     ~/.pogo/bin/net-control.sh  && chmod 0755   # was ABSENT

**Not** `pogo service install-deploy`. It is the supported installer and it would
have worked, but it also re-renders the plist and does `launchctl bootout` +
`bootstrap` — and its unconditional overwrite of this very file is the subject of
an open human-gated ticket (mg-3bb3). The narrowest action that makes launchd
execute the fixed runner is the copy: the plist is already correct, and leaving it
untouched is a stronger guarantee about what fires tonight than rewriting it and
checking afterwards. `InstallDeploy` copies the file byte-for-byte with no
substitution (`internal/service/deploy.go:330`), so the copy is not an
approximation of what the installer would have placed — it is the same bytes.

## The gates

**1. The installed artifact is now the shipped one.**

    git hash-object scripts/launchd/pogo-deploy.sh   958fa229057b4e5546f65d0716f2d66072c0b249
    git hash-object ~/.pogo/bin/pogo-deploy.sh       958fa229057b4e5546f65d0716f2d66072c0b249   ← equal
    git hash-object scripts/lib/net-control.sh       d8bba8d131794123cc9c563b03ca1ef888e918f8
    git hash-object ~/.pogo/bin/net-control.sh       d8bba8d131794123cc9c563b03ca1ef888e918f8   ← equal

**2. launchd still runs what it ran before — the same path, from an unchanged plist.**

    git hash-object ~/Library/LaunchAgents/com.pogo.deploy.plist
      a76532fdaff738c2d923566ad08166b0cc2873e8   before AND after — byte-identical
    ProgramArguments[0] = /Users/daniel/.pogo/bin/pogo-deploy.sh
    launchctl print gui/501/com.pogo.deploy → state = not running, runs = 17 (unchanged)

The job was not running at install time, so nothing was overwritten under a live
fire.

**3. The installed file parses and executes.**

    bash -n ~/.pogo/bin/pogo-deploy.sh   → OK
    bash -n ~/.pogo/bin/net-control.sh   → OK   (defines net_control, net_control_line, net_control_report)

A rehearsal in the plist's own environment, at 12:02 local — outside the window,
so it exercises startup and the terminal-line machinery and nothing else:

    env -i PATH=<the plist's PATH> HOME=$HOME POGO_HOME=~/.pogo POGO_DEPLOY_SRC=~/.pogo/deploy-src \
      ~/.pogo/bin/pogo-deploy.sh --dry-run

    [2026-08-19T11:02:19Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=true)
    [2026-08-19T11:02:19Z] window: local hour 12 is outside [2,6) — ... NOT deploying. Exit 0.
    [2026-08-19T11:02:20Z] pogo-deploy: end (rc=0 after 0s)
    exit=0

This added **nothing** to `~/Library/Logs/pogo/pogo-deploy.log`: the runner logs to
stdout and the log file is launchd's redirect, so a hand rehearsal cannot pollute
the record `internal/staleness/nofire.go` reads. (Its dry-run handling would have
excluded the lines anyway.)

**4. The fix is present in the bytes launchd will read.** `bounce|restart-fleet`
matches lines in `~/.pogo/bin/pogo-deploy.sh`: **45 → 106**, equal to the repo
copy's 106 on both sides of the install.

## Two things found while verifying, neither of them the ticket

**`~/.pogo/bin/net-control.sh` was absent.** `install-deploy` ships it as a pair
with the runner; this box had never had it, because the last install predates
`bdacc21`. Copying it was additive — no existing file, nothing overwritten. Had it
been left out, the fixed runner would have degraded honestly rather than failed
(`net_control=unknown`), but every transport-failure alert tonight would have
carried `no positive-control library found ... Run: pogo service install-deploy`.

**Tonight's bounce fallback will resolve at the *second* candidate.**
`resolve_bounce_script` prefers `$SRC/scripts/pogo-self-deploy` (the deploy
checkout) and falls back to `$BOOTSTRAP_REPO/scripts/pogo-self-deploy` (`~/dev/pogo`):

    ~/.pogo/deploy-src   f83e956 (2026-08-14)  --help advertises 'bounce': NO
    ~/dev/pogo           (2026-08-19)          --help advertises 'bounce': YES

The deploy checkout is three days stale and predates mg-9fc9, so it will be
skipped with the logged reason mg-9fc9 wrote for exactly this case, and the dev
tree will carry the bounce. That is the designed narrow exception working, not a
defect — but it means tonight's fallback depends on `~/dev/pogo` being current,
and `~/.pogo/deploy-src` only catches up on a night the fetch **succeeds**, which
is the night the fallback is not needed.

## Open follow-ups (not done here, deliberately)

1. **Nothing detects this drift.** `deployScriptInstallPath()` has writers and no
   comparator: `grep -rn 'deployScriptInstallPath' --include='*.go' cmd/ internal/`
   returns only `internal/service/deploy.go`. `pogo doctor` compares the *plist*
   against the shipped template (`cmd/pogo/launchagentdrift.go`) and says nothing
   about the runner's contents, which is the artifact that actually decides what
   runs. Today's gap was found by a human reading two `git hash-object` outputs.
2. **The first real 03:00 fire is unobserved.** This item proves the bytes; only
   the night proves the run.
3. mg-3bb3 (unconditional overwrite) and mg-49b5 (`GH_TOKEN` sourcing) remain
   open and human-gated. Untouched.

## Numbers that are not mine

pm-pogo measured `bounce|restart-fleet` as **136** in the repo copy and **45** in
the installed one. The 45 reproduces exactly (45 matching lines). The 136 does
not: this box measures **106** matching lines / **110** occurrences in `980048f`.
The direction, the drift and the conclusion are unaffected; the specific 136 is
pm-pogo's figure and was not re-derived here.
