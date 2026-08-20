- **`TestReadRealHost` asserted that some other process on the host was busy,
  and failed 7 of 15 CI runs on `main` when none was — the merge gate could not
  see it, because it runs on darwin and this only happens on Linux (mg-d54a).**

  The assertion was `UsedCores != 0` "on a host that is running this test", and
  its premise is false at the root: **the process running the test sleeps
  through the window it is measuring**. Measured on the real path on 2026-08-20,
  the test process's own CPU across a 200ms window is **34-162µs — zero ticks of
  a 10ms column, ten times out of ten**. Every nonzero that assertion ever saw
  came from *other* processes on a busy developer box. An idle 4-core GitHub
  runner has none, so the sample reads `fleet 0.0 of 4 cores across 0 procs,
  external 0.0` — resolved, attributed, and correct — and the test called it a
  defect.

  It is not `Unresolvable`: at 10ms resolution a 200ms window is resolvable five
  times over. Below `MinWindow` the instrument cannot separate work from none,
  and that case already reports a reason. **Above it, a zero is still a real
  reading** whenever nothing crossed a tick — the smallest nonzero a 10ms source
  can report over 200ms is 0.05 cores. `TestSubTickWorkIsAnHonestZero` now pins
  that combination (`Resolved()` true, `Attributed` true, `UsedCores` zero) and
  runs on every platform, which is the point: the state that only *occurs* on an
  idle Linux runner is now *asserted* on darwin too.

  `TestReadRealHost` now burns a core in a goroutine for the duration of the
  sample and asserts against CPU it spent itself — the pattern the refinery's
  gate watch already uses (`spinGate`). With the spinner the same window
  measures 19-20 ticks, ~1.0 cores, 10 of 10 — a ~20x margin over the one-tick
  detection floor, and it holds at `GOMAXPROCS=1` and under `-race`. No
  magnitude is asserted: cores are a shared resource and a floor on one is an
  assertion about the box, which is how four innocent branches went red in an
  evening (mg-6c90). Up to three samples are taken and any one nonzero settles
  it, so a starved scheduler costs a retry rather than a false red; a genuinely
  broken instrument still reads zero three times and still fails.

- **Recorded the direction of the CI/gate blind spot that hid it (mg-d54a).**

  `CONTRIBUTING.md` already warned that a green CI run is not evidence a gate
  failure is spurious. The converse is the more expensive direction and was not
  written down: a **green merge gate is not evidence that CI is green**, because
  a Linux-only path — `linux-procfs`, a procps `ps` format, a `/proc` read —
  does not execute on darwin at all. Seven failures interleaved with successes
  over six hours, and because they interleave, **a check that reads only the
  newest completed run reports GREEN**. `ARCHITECTURE.md` gains the sub-tick
  zero alongside the existing `Unresolvable` one, and the rule the two cases
  imply: a real-host CPU test asserts on CPU it spends itself, never on CPU it
  hopes to find.
