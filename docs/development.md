# Development

Pogo is a Go project. Binaries live in `cmd/`: `cmd/pogo`, `cmd/lsp`, `cmd/pose`, `cmd/pogod` (daemon).

## Build from source

```sh
git clone https://github.com/drellem2/pogo.git && cd pogo && ./build.sh
```

Requires [Go](https://go.dev/dl/) 1.25+.

```sh
./build.sh              # Format, test, build all binaries into ./bin
./build.sh --install    # ...and also `go install` them into GOBIN
./test.sh               # Run tests only
./fmt.sh                # Format code only
```

Always run `./build.sh` before committing. If it fails, fix the issue before pushing.

`./build.sh` builds into `./bin` (gitignored) and never writes GOBIN, so an
unattended build in an agent worktree or in the refinery's quality gate cannot
overwrite the installed `pogod`. Use `--install` to put binaries on your `PATH`;
set `POGO_BUILD_DIR` to build somewhere other than `./bin`.

## Where the build time goes

`build.sh` is the refinery's merge gate, so it runs at least twice per merge and
again on every retry, and it is what sets the fleet's per-repo concurrency cap.
Every run therefore ends with a ranked per-step profile:

```
=============================================================================
GATE STEP PROFILE — test.sh: 20 steps, 286.89s wall, 180.62s cpu

   rank       wall    share        cpu    cores    load  step
      1    113.84s    39.7%    121.69s     1.07    4.56  Testing Go packages
      2     51.12s    17.8%      2.59s     0.05    2.74  Testing the pogod condition annunciator (live controls: negative + A2)
      3     42.25s    14.7%      8.28s     0.20    3.30  Testing pogo-self-deploy live mail-check control
      4     25.90s     9.0%      2.59s     0.10    2.76  Testing the live control's sandbox isolation and setup-failure reporting
      ...
```

* **wall** — how long the step held the gate.
* **cpu** — CPU seconds (user+sys) of the step's reaped process subtree.
* **cores** — `cpu/wall`, how many cores the step kept busy on average.
* **load** — the host's 1-minute load average as the step *started*, so a row
  measured under fleet load is distinguishable from a quiet one.

`cores` is the column to read first, because it says which lever applies. A step
with a high `cores` is **compute-bound**: it contends with every other gate on
the host, and only doing less work makes it faster. A step with `cores` near zero
is **wall-clock-bound** — a sleep, a daemon boot wait, a poll interval — which
costs the gate its full duration and costs the host almost nothing, so it is the
cheapest thing to overlap or move off the merge path. Treat `cores` as a floor:
detached descendants that are never waited for are charged to nobody.

The `Testing Go packages` row is broken down further by
`scripts/go-test-budget.sh`, which prints how many packages ran, how many Go
served from cache, and the ten slowest.

The profile is on by default and costs a few milliseconds per step — the
interesting distribution is the one under fleet load, and that cannot be
requested after the fact. `POGO_GATE_PROFILE=0` suppresses the table;
`POGO_GATE_PROFILE_JSON=<path>` appends one JSON object per run to a file of your
choosing. There is no default file: the gate runs in ephemeral worktrees under a
developer's live `HOME`, and stdout is already captured for the run it describes.

A measured breakdown, and what it implies for making the gate faster, is in
[docs/investigations/gate-step-profile-2026-08-09.md](investigations/gate-step-profile-2026-08-09.md).

## Pre-commit hook

```sh
git config core.hooksPath hooks
```

The hook runs `gofmt -l` and `go build ./...` on every commit.

The `commit-msg` hook additionally rejects commit messages whose closing
keywords would shut a GitHub issue — including across a line wrap, which is how
a narrative body once shut an external contributor's issue by accident. Cite
issues as `Refs owner/repo#N`, or add `Closing-ref-ack: <ref> — <why>` when the
closure is deliberate. The refinery runs the same check on every merge, so this
hook is an early warning rather than the guarantee.

The refinery also reads the **pull request body**, not just the commits: a
`Resolves #N` there closes the whole issue when the PR merges, and no hook can
see it because it is in no commit. Cite the issue as `Refs owner/repo#N` in PR
bodies too, and fix a flagged one with `gh pr edit <number> --body-file -` —
amending and re-pushing does nothing to a PR body.

## Tests that measure a shared resource

**An assertion over a shared resource must be RELATIVE. Never require a minimum
share of one inside a fixed window.**

CPU cores, scheduler timeslices, and wall-clock throughput are shared with
everything else running on the box. A test that requires a minimum of one —
"at least 0.5 cores", "at least 2 frames in 400ms", "at least N samples in
T seconds" — is asserting what the scheduler happened to grant, not what the
code did. It is **unmeetable by construction under contention**, so there is no
correct threshold value and widening the number only buys silence: a control
tuned until it stops firing has stopped measuring anything.

**It is the fixed WINDOW that breaks it, not the counting.** A count is fine
when the window it is counted over stretches with the load. `sleep 400ms and
require 2 frames` is broken; `require 5 heartbeats over however long the gate
actually took` is not, and survived 90 competing spinners on a 10-core box —
its window stretched from 1s to 8-12s and the count came with it. Before
rewriting an assertion, check which kind you have.

This is not hypothetical. Two such assertions cost this repo five innocent
branches in one evening (mg-6c90), each a full gate run plus the work of
proving the branch was clean, and the coordinator ended it reading
`FAIL internal/refinery` as noise before checking — which is how a real
regression merges unnoticed. Byte-identical binaries measured 4/4 PASS at load
5 and 13/13 FAIL at load 52-106.

Write one of these instead:

- **Compare two arms.** Measure the thing busy and measure it idle; assert the
  ordering.
- **Track injected work.** Quadruple the work, require the measurement to rise
  with it. Ratios survive contention because schedulers allocate per runnable
  thread: N spinners get about N times one spinner's share whatever else the
  box is doing. This is the strongest form — it proves the number is a
  *function of* the work, so a constant, a wrong-subtree reading, and a lost
  descendant all fail it.
- **Wait for the event instead of budgeting for it.** Poll to a generous
  deadline rather than sleeping a fixed window and counting what arrived. On a
  quiet box it costs nothing; on a loaded one it takes longer instead of going
  red. It still fails when the event genuinely never comes, which is the defect
  worth catching.

**Run the arms CONCURRENTLY.** A relative assertion is only relative if both
arms met the same contention, and arms run one after another do not on a box
whose load is moving. Measured while this host's load ramped from 6 to 62, the
same rules that produced 3.68x–4.05x over six steady runs collapsed to 2.06x
against a 1.5x threshold — one arm sampled quiet, the next sampled saturated.
Started together they are squeezed by the same competitors over the same
windows, and the ratio holds by construction. This is the part that is easy to
skip, because a sequential version passes on a quiet machine.

Upper bounds — "this returned in under 2s", "one spinner did not exceed 2
cores" — are safe to state absolutely, because contention can only push a
measurement down.

**Do not reason from load average.** It counts threads that are runnable *or*
blocked, so it does not predict the share a process gets: on this 10-core host
a spinner held ~1.0 cores at a load average of 18–23, and 0.09 cores against 90
deliberately-runnable competitors. This is why the CPU test could pass at load
154 in isolation and fail at load 52–106 during a gate — the variable was never
the average, it was how many threads were actually runnable inside the
measurement window, which nothing in a test can see. Measure against a known
number of competitors instead.

Whatever you write, **show it can still fail**: construct the broken instrument
and watch the test go red. `internal/refinery/gatecpuarms_test.go` does this
three ways — a table of broken instruments against the rules, a live gate with
its subtree walk deliberately blinded, and
`TestTheReplacedFloorWasBothWEAKERAndFlakier`, which runs the retired assertion
and the replacement over the same measured arms to show the replacement is
strictly stronger rather than merely different. That last one is worth copying:
when you replace a control, the claim "this is better" is checkable, and a
replacement that is only *quieter* is a regression.

## End-to-end smoke test

`scripts/test-e2e.sh` exercises the full loop — `pogo init`, `pogod`, mayor
(the coordinator) auto-start, polecat (disposable worker agent) spawn,
refinery (merge queue) merge, gate-failure rejection, and crew
crash → respawn — against a sandboxed `$HOME`, a non-default port, and a
fake-agent stand-in for `claude`. No API keys required.

```sh
scripts/test-e2e.sh                  # ~30s; per-step PASS/FAIL summary
POGO_E2E_KEEP=1 scripts/test-e2e.sh  # leave the sandbox dir on disk to inspect
POGO_E2E_PORT=20000 scripts/test-e2e.sh
```

The test is also wrapped as a Go test, skipped by default so it doesn't slow
`go test ./...`. To run it through the Go toolchain:

```sh
POGO_RUN_E2E=1 go test ./internal/agent -run TestE2ESmoke -v -timeout 5m
```

Requires `mg` (macguffin, the task-store CLI) >= v0.1.3 on `$PATH`.
