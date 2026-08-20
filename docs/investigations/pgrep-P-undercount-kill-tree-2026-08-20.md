# `pgrep -P` undercounts by the caller's own branch — the two shipped call sites

**Work item:** mg-19e4 · **Date:** 2026-08-20 · **Status:** Both call sites resolved; the mechanism measured in both failure directions

Filed by mg-cbee, which established the mechanism (`pgrep` excludes the calling
process **and every one of its ancestors** unless passed `-a`, and `-P` is
subject to that same filter) and deliberately left two shipped call sites alone.
See `pgrep-cannot-see-pogod-2026-08-20.md` for the mechanism and its
measurements. Everything below is macOS (`Darwin 24.6.0`, bash 3.2.57); Linux
`procps-ng` is a different `pgrep` and nothing here was run against it.

## 1. Call site 1 — `kill_tree()` in `scripts/launchd/pogo-deploy.sh`

The walk enumerated children with `pgrep -P "$pid"`. Because pgrep removes the
caller's whole ancestor chain, **every node on the path from the walk's root
down to the calling shell is dropped, and so is every subtree hanging off those
nodes.** The walk then signals the root and returns success, having skipped a
subtree. That is the deadline watchdog's exact shape: `arm_run_deadline` forks
the watchdog as a child of the run it is bounding, so the watchdog kills the
tree it is standing in.

### The reproduction

`root → {leafB, mid → {leafA, caller}}`, with `kill_tree` invoked from `caller`.
`leafA` is the assertion: a leaf, not on the caller's path, reachable only by
descending **through** `mid` — the node the exclusion removes. Now
`scripts/pogo-deploy_test.sh`, run three ways.

| child enumeration | `KILL_TREE_SKIP` declared | survivors under root | caller |
|---|---|---|---|
| `pgrep -P` (shipped until now) | yes | **`mid`, `leafA`** | survives, walk completes |
| `pgrep -P` (shipped until now) | no | **`mid`, `leafA`** | survives — *by accident*, see §2 |
| `pgrep -aP` (the one-flag fix) | yes | none | survives |
| `pgrep -aP` (the one-flag fix) | no | none | **KILLED mid-walk** |
| `ps` + self guard (landed) | yes | none | survives |
| `ps` + self guard (landed) | no | none | survives |

Both failure directions are real, which is why `-aP` was not the fix: `-a` stops
excluding the ancestors **and** the caller, so it hands the kill loop its own
pid. What landed enumerates children from `ps -ax -o pid=,ppid=`, which applies
no filter at all, and refuses to signal the process the walk is running in
whether or not the caller declared `KILL_TREE_SKIP`.

### 2. The two defects were masking each other

`KILL_TREE_SKIP` is set with `KILL_TREE_SKIP="$(self_pid)"`, and `self_pid` was

```sh
self_pid() { sh -c 'echo $PPID'; }
```

which is **one fork too deep**: `$(self_pid)` forks a subshell to run the
function body and `sh` is that subshell's child, so `$PPID` names a throwaway
process that has already exited. Measured on bash 3.2.57 against the enclosing
shell's real pid (obtained without a command substitution — `sleep &`, then the
child's `ppid`), in six contexts: top level, plain subshell, doubly-nested
subshell, inside a function, a subshell with a TERM trap set, and a backgrounded
subshell. **It was wrong in all six**, by 5 to 9 pids.

So `KILL_TREE_SKIP` has been holding a pid that matches nothing, and what
actually kept the watchdog from killing itself partway through its own kill was
the pgrep ancestor exclusion — the defect above. Fixing either one alone breaks
the watchdog. The suite's existing assertion could not see it: it checked only
that `self_pid` differs from `$$`, and a wrong pid also differs from `$$`.

The landed form is `self_pid() { exec sh -c 'echo $PPID'; }`. `exec` replaces the
command substitution's own subshell, so `$PPID` is *by definition* the caller,
with no assumption about how many levels bash inserted. It is safe only because
every call site captures it with `$( )`; a corpus assertion in the suite holds
that.

### 3. Removing the blindness removed a rail

`pgrep -P 0` and `pgrep -P 1` came back empty for the same reason everything
else did — pid 1 is every caller's ancestor — so a stray root of `0` or `1` was
a no-op. `ps` answers that question honestly, and honestly is the whole machine.
Nothing in the runner passes either value today; `kill_tree` now refuses a root
that is not a positive integer, and says so. The suite checks the guard **in
source before exercising it**, because a regression test for this one destroys
the box it detects the regression on.

## 4. Call site 2 — `builds_in_flight()` in `scripts/launchd/pogo-reclaim.sh`

`n="$(pgrep -x "$name" ... | wc -l)"` over `go`, `compile`, `link` is a
defer-guard: non-zero makes the fire defer (exit 5) rather than reclaim disk
under a build. The exclusion is real here too — measured from a shell spawned by
a `go test`:

```
ancestors: /bin/bash <- p19e4probe.test <- go <- /bin/zsh <- claude <- pogod <- launchd
pgrep  -x go : []
pgrep -ax go : [24376]
ps count  go : 1
```

Empty, at exit 0 after `wc -l`, while the one `go` on the box was the caller's
own ancestor.

**On the production path it was already correct, and that is the first question
mg-19e4 was asked to settle rather than leave ambiguous.** `com.pogo.reclaim`
execs the script directly from launchd, so its ancestor chain is launchd and its
own bash and nothing else — never a `go`, `compile` or `link`. No Go test
executes the script (`internal/service/reclaim_test.go` only writes and copies
it), and `scripts/pogo-reclaim_test.sh` runs under `bash`, not under `go`. The
exposure is the operator's manual run (the README's dry-run line) and any future
caller reaching this script from inside a build.

It was fixed anyway, with `-a`, because the flag is bought cheaply here and the
error direction is the dangerous one. `-a` also stops excluding the caller, but
the caller is `pgrep`, which cannot match any of those three exact names — so
the only rows `-a` can add are true ones, and the error it can introduce is an
**over**-count, which defers a reclaim rather than firing one. The neighbouring
rule survives untouched: `-f` is still never used here, because a pattern
matched against full command lines matches half the fleet.

The suite's control for this is **live, not stubbed** — a symlink to `/bin/sh`
under a unique name gives an ancestor with a unique exact process name, and the
two readings then differ only in the flag. A stub could not establish a property
that belongs to the real `pgrep`.

## 5. Not established

- **Linux.** As for mg-cbee. `ps -ax -o pid=,ppid=` is portable to `procps-ng`
  and `pgrep -ax NAME | wc -l` still counts the right number there (`-a` is
  `--list-full`, one line per match), but neither was run.
- **A live deploy.** The `kill_tree` exposure is measured against a purpose-built
  tree, not against a nightly run that actually hung. The shipped runner's own
  topology puts the watchdog one level under the run, where the exclusion drops
  only the watchdog itself — which `KILL_TREE_SKIP` was meant to cover anyway.
  The undercount is a property of the function, reachable by any deeper tree; no
  claim is made here that a particular night lost a particular process.
- **Neither script's fix is live until it is reinstalled.** launchd runs the
  static copies under `~/.pogo/bin/`; a merge to main does not refresh them
  (`pogo service install-deploy`, `pogo service install-reclaim`).
