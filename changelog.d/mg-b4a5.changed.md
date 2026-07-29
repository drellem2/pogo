- **The last two hand-rolled test envelopes go through `scripts/pogo-sandbox` (mg-b4a5).** mg-78a5
  packaged the isolation envelope and converted `scripts/pogo-self-deploy_live_test.sh`; mg-0941
  converted the three Go suites onto `internal/testsandbox`. Two shell siblings still sourced
  `scripts/lib/sandbox-daemon.sh` directly and hand-rolled the rest — their own `mktemp` roots,
  their own `HOME`/`XDG_CONFIG_HOME`/`POGO_HOME` exports, their own kill-and-`rm` cleanups. Both now
  run inside the checked envelope, and the family has one implementation rather than three.

  **`pogo-self-deploy_sigint_test.sh` had already drifted, in the way that matters.** It never
  pinned `MG_ROOT`, and `mg` resolves its store as `--root` > `$MG_ROOT` > `$HOME/.macguffin` — so
  the real `cmd_redeploy` this control drives reaches the live mail store the moment it sends
  anything, and the only reason it never did is that the run aborts at the drain window, before the
  driver's first send. A control whose isolation depends on where the code under test happens to
  stop is not isolated; it is lucky. `pogo_sandbox_isolate` pins all four variables and *checks*
  each one against the developer's tree before an assertion runs. Its drain-state staging call now
  goes through `pogo_sandbox_curl`, so a precondition that never landed ends the run as
  infrastructure instead of being reported as an interrupt-safety regression.

  **`pogo-self-deploy_live_setup_test.sh` is the one that breaks the sandbox on purpose**, so only
  its scaffolding moved inside the envelope: its root, its fake pogod binaries and its `HOME`. Its
  three deliberate breaks still run in their own subshells or processes, because the refusal under
  test *is* `exit 99`, and it never reserves a port or starts a daemon of its own — nothing its
  setup depends on is a thing it deliberately breaks. The floor this buys is specific: §1 boots the
  real live suite end to end, and the failure this file exists to catch is that suite's isolation
  not working, so the child now inherits a sandbox `HOME` rather than the developer's on exactly
  the day it finds something. A new positive control observes that floor by writing through `$HOME`
  and reading back where the bytes landed. Its mute-daemon control now drives `pogo_sandbox_daemon`
  — the call every real caller makes since mg-78a5 — rather than reaching past it into the library.

  **Shown failing in both directions, for both files.** With the isolation deliberately broken
  (a sandbox root, then a sandbox `HOME`, symlinked onto a decoy developer tree), each exits **99**
  with the `SETUP FAILURE` banner and **zero `PASS:`, `FAIL:` and `PROVED:` lines**, leaving the
  decoy's `~/.pogo` untouched. With the code under test broken instead, each still fails as an
  **ordinary assertion**: a returning `SIGINT` handler in the deploy driver reddens the sigint
  control, and dropping the setup exit code, the readiness deadline, or the port reservation's
  `O_EXCL` reddens the setup control in three different places. The new positive control was shown
  red on purpose too, by moving `HOME` back out of the sandbox after setup.
