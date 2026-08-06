- **A sandboxed test suite no longer re-downloads the Go toolchain into the
  throwaway HOME it just created — measured at ~36 minutes inside a 60-minute
  refinery gate (mg-cdf1).** `scripts/pogo-sandbox` pins HOME, XDG_CONFIG_HOME,
  POGO_HOME and MG_ROOT under a private root so the deploy suites cannot read or
  write the live fleet's state. Go resolves its module cache off `$HOME`, so the
  instant that pin lands the cache is empty — and with `GOTOOLCHAIN=auto` and
  `go 1.25.0` in `go.mod`, the next `go` call decides it must upgrade and fetches
  the whole toolchain from Google into a directory teardown deletes.

  **It does not take a build to trigger.** The live specimen, caught mid-gate on
  2026-08-06, was a bare `go env GOBIN` — `scripts/pogo-self-deploy`'s
  `resolve_mg` runs two of them on every invocation, and the suite sources that
  script and drives its primitives under the sandbox HOME:

      go env GOBIN (21:03 elapsed)
       └ scripts/pogo-self-deploy_live_test.sh
         └ ./test.sh
           └ ./build.sh
             └ pogod            <- the refinery gate

      fd 4  .../toolchain/@v/v0.0.1-go1.25.0.darwin-arm64.lock
      fd 5  .../toolchain/@v/v0.0.1-go1.25.0.darwin-arm64.zip....tmp  (30.8 MB, growing)
      fd 12 TCP 10.90.70.189:57728->uv-in-f207.1e100.net:https (ESTABLISHED)

  The temp file grew 327,680 bytes in 10s — ~32 KB/s, so ~36 minutes for ~70 MB.
  `sample` showed it parked in `kevent`, waiting on I/O rather than spinning: not
  a deadlock and not load. **Cost: three consecutive 60-minute gate timeouts on
  one merge**, roughly three hours of a single-slot refinery other polecats were
  queued behind, each reported as `gate "./test.sh" exceeded its 1h0m0s timeout`
  — which reads as a verdict on the branch under test. It is intermittent only
  because it depends on whether the download finishes inside the gate's bound.

  **The obvious fix is wrong, and `go version` is why.** The ticket proposed
  `GOTOOLCHAIN=local` on the grounds that the installed Go is already 1.25.0.
  `go version` does print `go1.25.0` — but only because `GOTOOLCHAIN=auto` has
  *already* switched into the cached toolchain module. The toolchain actually
  installed on this box is **go1.24.0** (`/opt/homebrew/Cellar/go/1.24.0/libexec`,
  which `GOTOOLCHAIN=local go env GOROOT` reports), and `go.mod` requires 1.25.0.
  A bare `GOTOOLCHAIN=local` would have traded a slow sandbox for one where every
  build fails `go.mod requires go >= 1.25.0`.

  So the pin is two halves: `pogo_sandbox_isolate` reads the GOROOT the real
  environment has *already* resolved — while `$HOME` is still the developer's,
  for the same reason `real_home` is read there — puts that GOROOT's `bin` ahead
  on PATH, and only then sets `GOTOOLCHAIN=local`, so `local` names the toolchain
  `go.mod` actually requires.

  **Which isolation property this preserves, stated because a fix that bought
  speed with reach would be worse than the bug.** HOME, XDG_CONFIG_HOME,
  POGO_HOME and MG_ROOT are untouched, and every existing resolve-through-symlink
  check still runs against them. The sandbox keeps its own empty GOMODCACHE and
  writes nothing outside its root. Nothing here touches the port, the daemon, or
  which `base_url` the driver resolves — the suite's own assertion that *"driver
  resolves base_url to the sandbox daemon (not the live fleet)"* is decided by
  POGO_HOME and `$PORT`, neither of which moves. The single thing shared with the
  real home is the resolved toolchain's `bin` directory, **executed and never
  written**. Pointing GOMODCACHE at the real one would also have cured the
  download, and was rejected for exactly that reason: the sandbox *writes* to its
  module cache, and that write would land in the developer's.

  **Proved against a cold cache, which is the only state the bug exists in.** A
  sandbox HOME created seconds ago is cold by construction, so
  `scripts/pogo-sandbox_test.sh` §7 asserts both directions inside one such HOME:
  with the pin, `go env GOVERSION` answers with the real toolchain under
  `GOPROXY=off` and prints no `downloading` line; with the pin removed and
  nothing else changed, the same cold HOME does attempt the fetch. The second
  half is the positive control — without it the first proves only that this
  machine's cache happened to be warm. `GOPROXY=off` throughout is what turns
  "downloads 70 MB" into "says it would have" in milliseconds; without it the
  control would *be* the 36-minute stall.

  The pin also proves itself at run time rather than being assumed: `isolate`
  re-asks `go env GOVERSION` under the private HOME with `GOPROXY=off` and ends
  the run as a SETUP failure if the answer disagrees with the real environment's.
  An unproven pin fails as a 36-minute stall an hour downstream, in a gate,
  wearing the branch's name; this fails in milliseconds, at second zero, saying
  so.

  **Deliberately not changed: the deploy path itself.** `scripts/pogo-self-deploy`
  runs during the nightly redeploy under the real HOME with no `GOTOOLCHAIN`
  override, and its behaviour there is byte-for-byte what it was. The change is
  confined to the test-isolation harness, which the nightly does not use.

  **Overlap with mg-1bbf, noted rather than duplicated:** the out-of-band guard
  that refuses a redeploy from inside pogod's process tree would *not* have
  prevented this. It sits on `cmd_redeploy` only — `check` is deliberately
  unguarded, so the fleet can still notice its own drift — and the suites drive
  `check` and the sink controls, never `redeploy`. The gate-reporting half, where
  a gate that hangs on infrastructure must not emit a failure that reads as a
  verdict on the branch, is mg-e565 and is not addressed here.

  Still true, and out of scope: the sandbox's *module* cache is also cold, so a
  suite that compiled inside the sandbox would re-download its dependencies. The
  suites build under the real HOME before isolating, on purpose, and that comment
  now says which of the two fetches it is still protecting against.
