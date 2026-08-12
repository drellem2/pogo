- **`./build.sh &` stops failing the SIGINT control — the trap the control needs
  is now armed explicitly rather than inherited from whoever launched the gate
  (mg-a6c6).** `scripts/pogo-self-deploy_sigint_test.sh` failed **every time**
  when the gate ran as a shell background job — `./build.sh &`, `nohup sh -c
  '...' &`, `( ... ) &` — and passed in the foreground. Measured both directions
  on one tree in one minute: foreground `exit=0`, backgrounded `exit=1` with
  `rc=4`. Nothing was wrong with the branch under test.

  A shell without job control sets SIGINT and SIGQUIT to **SIG_IGN** for its
  *asynchronous* children, and that disposition crosses both fork and exec. So
  every process under `./build.sh &` — build.sh, test.sh, this control, and the
  sigtest it launches — entered with SIGINT already ignored, and bash will not
  install a trap for a signal that was ignored on entry (*"signals ignored upon
  entry to the shell cannot be trapped or reset"*). The deploy driver's `trap
  '...exit 130' INT` therefore never existed, the control's `kill -INT 0` did
  nothing, `cmd_redeploy` carried on into `do_build`, and the control reported
  exit 4.

  **The old failure message was correct and still misleading** — it reported a
  "lost/coalesced signal … control-harness delivery fault", which is exactly what
  happened. What it could not see, and did not say, was that the fault was the
  **launch context** rather than the code. An agent that backgrounds a long gate
  while it works on something else got a red result about its own invocation,
  and that is the ergonomic invocation, not an exotic one.

  **Bash cannot dig itself out, but the launcher is not bash.** `perl` already
  sat in front of the `exec` to do `setsid`, and perl *can* reset the
  disposition: `$SIG{INT} = "DEFAULT"` is a sigaction to `SIG_DFL`, and `SIG_DFL`
  is inherited across `exec` just as `SIG_IGN` is. The new
  `sigint_own_group_run` does that reset (INT **and** QUIT — the ignore rule is a
  two-signal rule) before `setsid` and `exec`, so bash comes up with a resettable
  SIGINT and arms its trap normally. The control now **runs** in every launch
  context rather than being skipped in some of them, so no sensitivity is traded
  away for the fix. This makes it *more* faithful, not less: what it models is a
  human at a terminal, and a terminal foreground process has SIGINT at `SIG_DFL`
  — the old form merely inherited that from the launcher, which is why the
  verdict depended on the launcher instead of on the code.

  **A third outcome, because two cells cannot hold three states.** A preflight
  probe now measures whether this harness can deliver a group SIGINT to a trap it
  armed itself, through the *same* launcher and in the *same* topology as the
  real control. If it cannot, the run reports **INCONCLUSIVE** — loudly, naming
  the cause and the remedy — instead of absorbing an instrument failure into the
  cell nearest to it. The probe never sources `pogo-self-deploy`, never calls
  `cmd_redeploy`, and signals only its own trap, so **a genuine regression cannot
  buy itself a skip**; that property is what the whole scheme rests on.

  **Mutation-checked, backgrounded, which is where the old control was blind.**
  Stripping `exit 130` from the driver's INT handler (the returning-handler bug
  this file exists to catch) fails with `exit=1`; deleting the `trap ... INT`
  line entirely fails with `exit=1`; both report **0 inconclusive**. Disabling
  the new reset reproduces the original trap and is correctly reported as
  INCONCLUSIVE rather than as a defect.

  One message was retired by its own fix: the "no signal arrived" branch used to
  blame the harness, which was the only explanation available at the time. Since
  the run can no longer reach it unless the preflight has already proven delivery
  works, it now says what it actually means — the driver's INT handler did not
  run.

  This is the **second** disposition-inheritance trap the fleet has hit, after
  mg-9aa1's SIGHUP finding; the reset here is the SIGINT analogue of the
  `signal.Notify` reset used there. The argument for fixing rather than
  documenting is that the next guise will not look like either of them.
