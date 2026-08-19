- **`./build.sh` failed on PRISTINE MAIN for six days, and the failing assertions
  were reporting THEIR OWN SUBSTRATE in the control's voice (mg-12aa).** The gate
  exited 1 on `f83e956` with no change applied — measured four independent ways
  by pe52c, all agreeing at `net-control.sh: 17 passed, 3 failed` — so every
  merge request into this repo failed the gate regardless of its content, and the
  failure was attributed to the branch. Exactly one pogo-repo merge was attempted
  in the three days before this and it failed; 17 items were queued behind it.
  mg-15bb is the shape of the damage: the classifier named a *passing* sandbox
  assertion as the cause and never reached the step that actually failed.

  **The three failures were one fault.** All of them handed the control a set of
  addresses believed to be blackholed and required a RED:

      FAIL: the control did not go red against three blackholed addresses (exit 0)
      FAIL: a single unreachable target produced something other than unknown
      FAIL: a dead IP reference set ... did not resolve to UP (exit 0)

  The addresses were not blackholed. Measured 2026-08-19 from a polecat worktree,
  with the VPN on `utun4` holding the default route:

      192.0.2.1:443     CONNECTED 0.09s   RFC 5737, routed nowhere
      198.51.100.1:443  CONNECTED 0.09s   RFC 5737, routed nowhere
      203.0.113.1:443   CONNECTED 0.09s   RFC 5737, routed nowhere
      240.0.0.1:443     CONNECTED 0.09s   reserved Class E — NOTHING can answer it
      100.64.1.1:443    no answer 3.0s    outside the tunnel's route set

  `connect(2)` is completed by the tunnel itself. A destination that cannot exist
  answers in 0.09s, so the suite's blackhole was not a blackhole and the control
  was right to say UP. **The suite named a substrate and took the naming for the
  arranging** — the assumption its own section 2 already refuses to make about
  its isolation mechanism ("prove the isolation before trusting what it
  produces"), left out of the sections that needed it just as much.

  **What ships is a substrate that is measured, not named.** A new section 2b
  establishes a blackhole with the control's *own* probe primitive and requires
  it to be one in both senses: nothing answers, AND nothing answers *slowly*. A
  refusal satisfies the first and not the second, and only the second is the
  mg-964e shape section 3 exists for. It prefers the real off-box RFC 5737
  addresses when they genuinely swallow a SYN — that path is exercised and green
  — and falls back to a **loopback SYN sink** when they do not: a listener with
  `listen(1)` and no `accept()`, whose full accept queue makes the kernel DROP
  further SYNs instead of refusing them. Not a mock; the same observable, made by
  the real kernel on real sockets, and unanswerable because there is no path.
  (`listen(1)` is load-bearing and not a small number: with `listen(0)` six
  connects in a row completed on Darwin.) If neither can be established the suite
  FAILS rather than skipping, per its own standing bar.

  **A remedy is an artifact of the same kind as the defect, so the new assertions
  were driven red before they were trusted.** Section 3 now bounds its sweep from
  BOTH sides — the existing `< 30s` upper bound, and a new `>= 3s` lower bound,
  because three DROPPED SYNs at a 2s budget cannot come back in no time and a
  sweep that does was talking to a substrate that REFUSED. Observed failing
  against three closed loopback ports: `the sweep returned in 0s, too fast for
  three dropped SYNs`. The substrate check itself was observed rejecting those
  same closed ports (`15 passed, 4 failed`, naming the substrate in all four)
  rather than accepting a fast refusal as a blackhole. Both branches of 2b run
  green on this host: 22/0 via the loopback sink, and 22/0 via off-box addresses
  when the pre-check is pointed at a range the tunnel does not carry.

  **The suite has no recorded green in the refinery's retained history.** It
  landed 2026-08-12 (`bdacc21`) and `~/.pogo/refinery-state.json` holds only
  `14 passed, 4 failed` (×3) and `17 passed, 3 failed` (×1) for it. The commit
  that introduced it claims the RED was OBSERVED, and it was — section 2's
  kernel-enforced red passes here and always did. What was never observed is the
  *blackhole* red, because the substrate that was supposed to produce it never
  had to prove itself.

  **The finding the fix does NOT repair, said where it will be read.** On this
  host the control's `up` currently means "utun4 is up", not "this box reaches
  anything" — the transparent-proxy false GREEN its own limits section names,
  no longer hypothetical. The suite now measures that condition and prints it
  beside its own green, twice, including next to the pass/fail count. The verdict
  itself is left as `up`: the obvious remedy, a reserved-address canary on every
  call, costs a FULL per-probe timeout on a healthy box — where the canary
  correctly blackholes — and this control has to be callable from an alert path.
  That trade is filed rather than made here.

  Cost, from the gate's own profile on the green run: the step goes from 8.93s to
  **31.49s wall / 1.21s cpu / 0.04 cores**. The added time is the fix — it is
  spent waiting for SYNs that are genuinely dropped, which is exactly what the
  old 0s never did — and by the profile's own vocabulary a step at 0.04 cores is
  WALL-CLOCK-BOUND: it costs the gate its duration and the host almost nothing,
  so it contends with no other gate on the box. Whole gate: 689.48s, exit 0.
