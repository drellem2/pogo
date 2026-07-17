- **The self-deploy SIGINT drain-window live control (section g) is no longer a
  bash-3.2 timing flake (mg-e91e).** The control simulated a Ctrl-C by firing a
  single-target `kill -INT $$` at the parent from inside a `$(drain_wait)`
  command-substitution child that then returned 0 cleanly. Whether the parent's
  pending INT trap ran (exit 130) or the signal was coalesced/lost as the child
  returned was a signal-delivery race — green under light load, but
  deterministically RED under the full control suite, where it observed exit 4
  and blocked *every* redeploy through `do_prove`. The simulation now models a
  real terminal Ctrl-C faithfully: it signals the whole foreground process
  **group** (`kill -INT 0`, with the child launched into its own group via `perl
  setsid` so the blast radius stays inside the sigtest tree), so the child dies
  instead of returning 0 and the clean-return race is gone. A new **positive
  sub-assertion** reads the parent trap's own message from stderr, so a
  lost/coalesced signal fails LOUD as "never delivered" rather than masquerading
  as a returning-handler verdict. The interrupt-safety property under test —
  SIGINT ⇒ exit 130 abort, dispatch restored, no carry into `do_build` — is
  unchanged and still enforced. Also fixed the control-suite temp-dir cleanup,
  which failed `rm: Permission denied` on the read-only Go toolchain module cache
  that `go env`/`go version -m` materialise under the sandbox `HOME`, littering
  `/var/folders` each run: it now `chmod -R u+w`s the sandbox before removing it.
