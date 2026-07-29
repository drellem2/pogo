- **A redeploy now proves its failure detector against the binary it just built,
  or refuses to restart the fleet.** The build step ran an install and no tests,
  while the control that proves the mail-check detector can actually fail was wired
  into the test suite — so it ran at merge time and never at deploy time, and every
  redeploy shipped a daemon whose detector had never once been exercised against
  it. The deploy now runs that control after the build and before the restart, on
  every run including restart-only ones, and refuses unless it observes the check
  go red against a real reap and green against an intact fleet. It asserts on
  markers the control emits at those two points rather than on an exit status,
  because exiting zero only means nothing that ran failed and cannot tell a run
  that demonstrated both directions from one where the check was skipped or never
  reached. A missing control is a refusal, not "nothing to prove". The check costs
  about two minutes inside the drain window, and a refusal leaves the running
  daemon untouched — the fleet keeps working rather than going down. Honest limit:
  this shows the detector works, not that its coverage is complete.
