- **The refinery's network retry budget was 52 seconds against an outage
  measured at 15m26s, and a CLEAN 8m58s gate was thrown away to it
  (mg-c3b7).** On 2026-08-10, MR `mr-d9sk3matjv1sgaptna70` finished `./build.sh`
  clean after 8m58s at 03:40:07Z, then lost the fetch to
  `ssh: connect to host github.com port 22: Undefined error: 0`, burned all 5
  network-class attempts over 52s of backoff by 03:41:00Z, and resolved
  `failed(infrastructure)`. Everything except the budget worked — the
  classification was right, the mail went out, the author resubmitted instead of
  chasing a phantom defect. The work was still lost, and re-run from scratch.

  **The budget is now sized by the measured DURATION of the event.** A
  controlled sampler ran across that exact window at 20s intervals carrying a
  positive control (`ping 1.1.1.1`: clean before onset, LOSS for the whole
  window, clean after recovery — so the instrument demonstrably can succeed and
  its failures carry information). Onset 03:37:23Z, recovery 03:52:49Z,
  **duration 15m26s**. The refinery's own timeline sits inside that window and
  agrees to the second: the gate passed because it finished 2m44s *into* the
  outage, and the fetch failed for the same reason. 52 seconds against 15m26s is
  short by ~17.8x, so no rearrangement of backoff inside 52 seconds could have
  worked — it could only fail faster. The schedule is now 15s/30s/60s then a 2
  minute plateau, `networkMaxAttempts = 14`, which sleeps **21m45s** in total,
  under a 22-minute clock backstop. The plateau bounds how long after recovery a
  merge stays asleep.

  Sized by duration and deliberately **not** by predicted timing: onset came
  1.6 minutes early against the prospective call, and the period is not
  established tightly enough to schedule against. Duration is what has to be
  survived.

  **Correction (mg-7110): 15m26s is one wave, not the size of the event — and
  the budget no longer covers the event.** This entry originally called the
  duration "stable across four waves" and claimed a **6m19s** margin. Both came
  from the first three waves of a recurring DHCP fault (mg-964e), which happened
  to cluster. The duration is a distribution and it has **widened twice**: the
  "15 min ±41s" summary was withdrawn by the agent that produced it after a
  17m52s wave, and wave L then ran **~35m03s** (n=12), with a ~28m floor
  corroborated independently of the lease by three consecutive missed `*/10`
  mail-check fires. So the shipped 21m45s has no margin at all — it is 13m18s
  short of the observed maximum. **The budget shipped here is unchanged and is
  now known-insufficient**, tracked as **mg-682d**; it still covers all eleven
  earlier waves and remains a large improvement on the 52 seconds it replaced.
  What this correction fixes is the *justification*, which claimed headroom that
  never existed at the size stated and does not exist at all today.

  **A completed gate verdict is now held across the wait instead of being
  discarded.** Every network step except the first `fetch origin` runs *after*
  the gates — `fetch-target`, `reset-target`, the ff-only merge, the push — so a
  socket that fails there does not cost a retry, it costs the entire gate run, at
  the most expensive possible moment. A passing gate now records the **tree
  object** of the rebased branch (`git rev-parse HEAD^{tree}`), and a retry that
  rebases to the same tree replays that verdict instead of recomputing it.

  The key is content-addressed, and that is the whole safety argument rather than
  a convenience: an identical tree means the re-fetch and re-rebase reproduced
  byte-identical content, so re-running the gate would compile and test the same
  bytes for the same answer. The moment the target or the branch moves, the tree
  differs, the hold does not match and the gates run again — nothing decides to
  trust a stale verdict. It is strictly stronger than the `[gates]
  skip_on_retry` knob that has shipped for much longer, which skips on
  `attempt > 1` whatever the tree says. An unreadable tree takes no hold at all:
  failing closed costs a re-run, failing open could land an ungated tree, which
  is worse than the nine minutes this saves.

  The acceptance test induces a REAL post-gate transport failure against a real
  git — a pre-receive hook rejecting the first push with the incident's verbatim
  ssh wording — and asserts the gate ran **once** across the retry, not twice.
  It ships with the constructive control that makes that assertion mean
  something: with another merge landing on the target during the gate, the gates
  must run **twice**, because the tree that would land is no longer the tree
  that was gated. Without it, "never re-run the gates" would pass.

  **The failure mail now states how long the refinery actually waited.** It
  reported the attempt count and the budget's own wording, and those cannot
  separate "the network was down longer than anyone could wait" from "we did not
  really wait" — on 2026-08-10 the 52-second figure was only in the refinery
  log. The line is summed from the per-attempt records rather than a new field,
  so it is equally correct for merge requests already on disk, and an unretried
  failure says so explicitly instead of reporting `0s` as though it were
  measured patience.

  **Two existing tests changed, and the reason is behavioural, not cosmetic.**
  Both injected their race with `git commit --allow-empty` — CI moving the ref
  without changing a byte — which is now precisely the case the hold absorbs.
  `TestProcessMergeFFRetryOnRace` asserted the gate ran *at least twice*; it now
  asserts *exactly once*, which is the saving. `TestProcessMergeMaxAttemptsConfigurable`
  used a perpetual empty-commit race to exhaust `max_attempts`, and that race is
  no longer perpetual — it converges, so the knob under test stopped being
  exercised at all. Its sidecar now commits real content, which is a race the
  hold must refuse, restoring what the test is for.

  **What is NOT claimed.** Nothing here predicts when the next window opens, and
  the fix does not depend on any periodicity claim. The 21m45s figure is not
  measured against a *future* outage — it was sized against the longest one
  observed at the time (17m52s), and outages materially longer than that have
  since occurred and do exhaust it (~35m03s, mg-682d). The mail says how long
  the refinery actually waited, in a way a reader can check against the event.
  This is a bound that has already had to move twice, not a settled constant.
  A held
  verdict also means the gate's *commands* do not re-run, so a gate with side
  effects outside the checkout fires once per distinct tree rather than once per
  attempt — intended, and the mechanism by which the compute is saved, but a
  real change for anything counting gate invocations.
