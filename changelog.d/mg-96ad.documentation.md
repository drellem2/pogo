- **The coordinator prompt learns pogod's own lifecycle, and every agent stops
  being told a falsehood about its own name (mg-96ad).** Two corrections to the
  shipped prompt corpus, both about things an agent could previously only get
  wrong.

  **Daemon lifecycle.** `internal/agent/prompts/mayor.md` documented restarting
  *agents* thoroughly and restarting *pogod* not at all — the one operation the
  coordinator would look up and the one place it was absent. The new section
  separates **restart** (bounce the daemon; same binary, activates zero merged
  commits; `pogo server start`/`stop`/`status`, and there is no `pogo server
  restart`) from **redeploy** (rebuild, reinstall, restart — the only thing that
  moves pogod onto merged code). It then states what is reachable: `scripts/
  pogo-self-deploy check` is safe from anywhere and never acts; drift usually
  needs no action because `com.pogo.deploy` redeploys nightly at 03:00 where it
  is installed; self-redeploy is impossible because `assert_out_of_band` refuses
  any caller pogod spawned; hand-off to `human` is the exception, with a stated
  trigger and `~/Library/Logs/pogo/pogo-deploy.log` as the evidence to carry.

  **It points at the guard rather than restating it.** The refusal message in
  `assert_out_of_band` already explains itself, including why the redeploy is
  legitimate even when the caller is not. A second copy in a prompt is a copy
  that can drift from the one that actually fires (mg-1bbf is the incident where
  the constraint existed in prose and nothing enforced it). The stop surfaces
  get the opposite treatment for the opposite reason: `pogo server stop` and
  `launchctl kickstart -k` are **unguarded**, so the prompt says so plainly and
  says that documentation informs rather than enforces.

  **`pogo-crew-<name>` is a display label, not a process name.** mg-ccd1 fixed
  the docs half; the prompt half is fixed here, at all ten sites — `mayor.md`,
  `crew/doctor.md`, `pm/pm-template.md`, all six polecat templates, and the
  Go-embedded minimal template in `prompt.go` that a walk of `prompts/` cannot
  see. Nothing sets these strings on any process, so `pgrep -f pogo-crew-mayor`
  against a live mayor matches nothing — and the failure is not that the lookup
  is unavailable but that it *succeeds and returns empty*, which reads as "the
  agent is gone" (mg-710c, mg-de08). `ARCHITECTURE.md`'s "Process Naming"
  section, `README.md`'s `pgrep pogo-crew` discovery line and the research-triage
  example template carried the same claim and are corrected too.

  **The deliverable is the ratchet, not the sweep.** This claim has been counted
  four times by three agents at three, five and thirteen sites, and each sweep
  found sites the previous enumeration missed — every number a fact about the
  search rather than the corpus. `TestProcessNameClaim_ShippedPromptsHoldTheLine`
  makes the allowed count zero with no grandfathered inventory, and ships with
  two refutation controls: one pinning the exoneration boundary (the corrected
  prose must pass, or the check punishes its own remedy) and one reintroducing
  the claim into a copy of the real prompt tree to show the guard fires there
  and not only on a hand-built fixture.
