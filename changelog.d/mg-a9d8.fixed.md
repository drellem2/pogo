- **Four suites already pinned `GOMODCACHE` at the developer's real cache by
  hand, with the download path open; the sandbox now does it once, for
  everybody, with the download path CLOSED (mg-a9d8).** That is the frame,
  because the header of `pogo_sandbox_create` recorded that this pin had been
  CONSIDERED AND REJECTED — the sandbox writes to its module cache, and pointed
  at the real one that write lands in the developer's. The rejection had already
  lost, quietly and unrecorded, at four call sites:
  `scripts/check-staleness_test.sh:61`, `scripts/test-e2e.sh:92`,
  `scripts/pogo-deploy_test.sh:2791` and
  `scripts/pogo-self-deploy_live_setup_test.sh:102`, none of which closes the
  download path. So this is not a documented decision being overridden; it is
  four unmanaged overrides being replaced by one managed one that writes less.

  **The defect it fixes.** `pogo_sandbox_isolate` moves `HOME`, and Go resolves
  `GOMODCACHE` off `$HOME` — so every build inside a sandboxed suite ran against
  an EMPTY module cache and had to fetch what it imports from
  `proxy.golang.org`. On 2026-08-14T03:46Z the resolver blinked mid-fetch and the
  `Testing the $TMPDIR leak guard` gate row failed as

      internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17: Get
        "https://proxy.golang.org/nhooyr.io/websocket/@v/v1.8.17.zip":
        dial tcp: lookup proxy.golang.org: no such host
      FAIL  github.com/drellem2/pogo/internal/agent [setup failed]
      Test 5: repeated runs do not grow the testtmp root
        FAIL: SETUP: the fixture-creating suite did not pass on the third run

  — no assertion failed, and `internal/agent` reported `ok ... 358.827s` in the
  whole-tree run of the SAME gate (both figures p82a6's, from the ticket). It was
  classed `DEFECT`, which commits to "re-running establishes the same fact"; for
  weather that is false.

  **How much network, measured — and a figure withdrawn.** Against a `file://`
  proxy served from a clone of the real cache, so the count is exact and nothing
  left the box: one run of `scripts/tmpdir-leak_test.sh` fetches **2 module zips
  and 2 `.mod` files, 752 KB, once.** The ticket carried "the whole module set is
  re-fetched six times in a single gate run" (mg-b463); that is **withdrawn** —
  the file makes six `go test` invocations but they share one sandbox `HOME` and
  therefore one cache, so only the second fetches and the other five read what it
  left. The mechanism is real; the multiple was never measured. What the same
  measurement adds is sharper than what it removes: the five-package slice
  imports **no external module at all**, so every byte is `./internal/agent` —
  the only package in this suite with external imports. This failure mode could
  never have named anything else.

  **What ships.** `isolate` pins `GOMODCACHE` at the cache the real environment
  already holds — captured before the `HOME` move, from the same `go env` call as
  the toolchain pin — and sets `GOPROXY=off` with it. Both halves are read back
  under the private `HOME` and the run ends loudly if either did not take, which
  is the rule the toolchain pin (mg-cdf1) already follows; a control exercises
  that loud failure rather than trusting the code that raises it. It fails OPEN
  when there is no cache to share, so a cold or foreign box behaves exactly as it
  does today — also pinned by a control, not by prose. With no cache to fill,
  `./internal/agent` builds under a cold sandbox `HOME` with the network refused
  entirely.

  **The write hazard, measured rather than argued.** `GOPROXY=off` removes the
  download path, so there is nothing to write. An APFS clone of this box's
  1.2 GB / 48,124-entry cache, pinned into a sandbox, then a cold-`GOCACHE`
  compile and run of the leak-guard slice plus `./internal/agent`; every entry's
  mode, size, mtime and ctime `stat`'d before and after: **zero changed, zero
  added.** The one write that survives `GOPROXY=off`, found by going looking for
  it and stated in the harness rather than left to be discovered: a module whose
  zip is in `cache/download` but whose tree is not extracted is extracted on
  demand, offline, writing that module's own `go.sum`-verified contents. It does
  not arise in the state the gate runs in — `test.sh` runs the whole-tree
  `go test ./...` under the real `HOME` at step 2, before any sandboxed suite.

  **The cost, stated.** A module in neither the extracted tree nor
  `cache/download` now fails inside the sandbox with `module lookup disabled by
  GOPROXY=off` rather than being fetched. That is deterministic and offline
  rather than weather, and one `go mod download` under the real `HOME` fixes it —
  but it still lands as `[setup failed]` in whichever package imports it, which
  is why the second half of this ticket is in the same branch.

- **A Go module failure inside a sandboxed suite says what it is instead of
  wearing the name of the package that imported the module (mg-a9d8).** New
  `pogo_sandbox_go_module_failure LOGFILE` translates a captured log into either
  "NETWORK failure fetching modules" or "MODULE CACHE MISS" — naming the pinned
  cache, saying whether re-running can change the answer, and stating plainly
  that the package the compiler named is not at fault.
  `scripts/tmpdir-leak_test.sh` runs its three setup-failure branches through it,
  so the report no longer opens with "SETUP: the fixture-creating suite did not
  pass" over a log whose content is `lookup proxy.golang.org: no such host`. That
  misattribution cost a held worker slot (mayor, 2026-08-14 03:57Z), a withdrawn
  hypothesis (pm-pogo) and an initial mis-scoping (p82a6). The refinery learned
  to classify the network case for itself in mg-67c9; this is the same repair one
  layer down, in the words a person reads first. Both directions are controlled:
  an ordinary assertion failure is NOT translated, so a real bug cannot be
  relabelled as weather.
