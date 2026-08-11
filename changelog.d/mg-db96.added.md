- **The nightly runner has a positive control, and it has been observed going RED
  (mg-db96).** `scripts/lib/net-control.sh` probes a reference set that has
  nothing to do with the deploy remote — three anycast resolvers, three
  operators, three networks, by literal IP — so a transport failure can finally
  be read as either *this box is off the network* or *this one remote is
  blackholed*. Until now the runner probed exactly one endpoint and those two
  states reached the reader as the same sentence.

  **The bar was the red, not the green, and it is met by two different real
  deprivations.** A positive control that has only ever been seen going green is
  not known to work. `scripts/net-control_test.sh` puts the control on a box with
  no network and requires DOWN: on Darwin via `sandbox-exec` with
  `(deny network-outbound)` and an allow for localhost — off-box `connect(2)`
  refused by the kernel while loopback still routes, which is the same observable
  state as a downed NIC — and on Linux via `unshare -rn`. Nothing is stubbed for
  that section. A second section drives it against three RFC 5737 addresses,
  because EPERM fails fast and a real SYN blackhole fails slow, and that slow
  shape is the one this host actually has (mg-964e). If neither isolation
  mechanism is available the suite FAILS rather than skipping: an unproven
  positive control is worse than none, because its green gets cited.

  **The control has its own positive control, run on every sweep.** Before any
  reference target is probed it proves its primitive can report BOTH a reachable
  endpoint and an unreachable one, against loopback — which survives the
  condition being measured. `pogo-deploy.sh`'s `resolve_nc` proves only the *no*
  direction, and a primitive that can only ever say no yields a control that is
  stuck RED, permanently and silently, under a plausible-looking per-target
  table. Every path that cannot complete lands on `unknown`, never `down`, and
  the self-test line is printed on the failed runs too — so a reader tells a true
  negative from a broken instrument by reading the report, not by trusting the
  verdict.

  **Three defects found by running it rather than by reasoning about it.** Pointed
  at three dead addresses on a demonstrably healthy box the first version reported
  DOWN — right about its targets, wrong about the box; reaching a host by NAME
  requires both resolution and a handshake, so the name arm now overrides an empty
  IP arm and says the reference set is what should be checked. Emptying an arm
  crashed the control outright under `set -u`, because bash 3.2 — what macOS ships
  and what runs this at 03:00 — treats an empty array's `"${a[@]}"` as unbound. And
  the missing-library stub captured its path list in a `local`, which is out of
  scope by the time the stub runs: a bash function body is not a closure.

  **What it does NOT do, deliberately: it does not change classification.** Making
  `network` conditional on the control belongs with the drellem2/pogo#130 fix
  (mg-0218), which is what makes the classification honest in the first place.
  What lands here is the evidence — the verdict goes into the deploy log, the
  abort alert (report, per-target table and all) and the `net_control` field of
  `deploy_nightly_retry_pending`, on the quiet exit as well as the alerting one.

  **It also withdraws a number that was being quoted as a measurement.**
  `remedy_for_sync_class` tells the reader to READ THE VIGIL DURATION AS A
  MEASUREMENT and offers it to mg-5515 as a lower bound on how long the transport
  was unreachable. The vigil re-probes one endpoint, so it is a lower bound on how
  long *that endpoint* did not answer — a statement about this box only if the box
  was off the network, which is the question the control answers. The alert now
  carries a sentence, keyed on the verdict, that licenses that reading in the one
  case where it holds and explicitly withdraws it in the other two. This is the
  third instrument in this fleet to have had the same defect, after doctor's
  discarded 379-probe gateway figure and the vigil duration itself.

  **`pogo service install-deploy` installs the library beside the runner.** The
  nightly does not run out of the repo — the script is copied to `~/.pogo/bin/`
  precisely so it keeps working while the checkout is mid-fetch or broken — so the
  library is copied there too, and the runner looks for a sibling first and the
  repo layout second. A missing library is not fatal to the install and not silent
  in the runner: the control reports `unknown`, names every path it tried, says
  what the absence costs, and names `pogo service install-deploy` as the fix.
