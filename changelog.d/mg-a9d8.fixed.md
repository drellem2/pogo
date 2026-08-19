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

- **Scope correction to the entry above, measured (mg-48d4).** `fa8a4e9`'s
  subject line ends "and a DNS blip stops being a failure in `internal/agent`".
  Read as a statement about the merge gate — "a failure in `internal/agent`" is
  the gate symptom from mg-b463 and the 08-14 incident — that reads as a claim
  about the gate's **package-test row**, and the pin is not on that row's path.
  The pin lives in `scripts/pogo-sandbox`; the package row is `test.sh:64`,
  `tmpdir-leak-guard.sh` -> `go-test-budget.sh ./...`, and that guard exports
  `TMPDIR` and nothing else. `scripts/pogo-sandbox` appears in `test.sh` once, at
  line 120, as the SUBJECT of its own suite. What the entry above fixes is the
  sandboxed suites, which is what its body says and what the 08-14 failure
  actually was: the row that went red was `Testing the $TMPDIR leak guard`, and
  `internal/agent` was the package NAMED IN a sandboxed suite's compile error,
  not the gate's package row failing.

  The clause is nonetheless TRUE of the package row — and for none of the
  reasons on offer, which is the part worth having written down:

  - **The row's own compile needs no network.** `HOME` is untouched by the
    guard, so `GOMODCACHE` is the developer's real cache: all 615 packages of
    `go list -deps -test ./...` resolve with `GOPROXY=off`, exit 0.
  - **The row is NOT fetch-free, and the pin does not cover it.**
    `internal/agent`'s `TestMain` pins `HOME` under a throwaway root
    (`internal/testsandbox`, whose own comment records this), Go resolves
    `GOMODCACHE` off `$HOME`, and
    `TestAgentPackageDoesNotImportRefinery` shells out to `go list` — so it runs
    against an EMPTY cache and downloads for real. Counted against a local
    404-ing proxy on the gate chain run verbatim: **37 module requests per gate
    run, every one of them from that single test**, at the ambient `GOPROXY`,
    which on this path is still the default `proxy.golang.org,direct`. The
    counter was proved with a positive control first (seeded toolchain, empty
    module cache: 38 requests, naming this package's two external imports).
  - **A blip there still does not fail it, for a narrow reason.** `go list`'s
    `.Imports` is read from the main module's own source, so a failed download is
    logged to stderr and changes neither the answer nor the exit status: the
    identical 44 imports come back with the path open, 404-ing, or `off`. The
    whole `test.sh:64` chain exits 0 with **zero** `FAIL` lines against a
    resolver that cannot resolve (2 full runs, 6m34s and 6m37s).

  So the honest sentence is *the query does not need the module*, not *the cache
  was warm* and not *the sandbox pin covers it*. That is now a control rather
  than a paragraph — `TestTheImportQueryDoesNotDependOnTheDownloadPath`, beside
  the two tests it protects, closing the download path against a cold `HOME` of
  its own so it cannot pass by reusing the cache the tests above it warmed.

  Provenance: the invocation chain, `fa8a4e9`'s file list and the guard's missing
  pin are pm-pogo's, filed 2026-08-19; the 615/37/38/44 counts and both dead-
  resolver chain runs are mg-48d4's, measured the same day. pa9d8's "752 KB, once
  per run" for the sandboxed suite is theirs and was NOT re-derived here.
