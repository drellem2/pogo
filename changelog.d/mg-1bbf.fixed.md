`scripts/pogo-self-deploy redeploy` now REFUSES when its caller is inside
pogod's process tree. The script's header has forbidden this since mg-6afa —
"must run OUT OF BAND — not as a descendant of pogod" — but nothing checked it.
Sixteen refusal paths existed for unreadable registries, drift, failed controls
and a silent pogod; the one prohibition the script stated about itself was the
only one with no mechanism behind it, enforced solely by whether the caller read
an 1800-line preamble.

That is the wrong enforcement for this population. A human reading the header is
plausible; an agent told "run the redeploy" is not — and every crew agent and
every polecat is a pogod descendant, so the actor most likely to skip the prose
is exactly the actor the prose exists to stop. What stopped it on 2026-07-21 was
a mayor happening to read the header and decline. Nothing else would have. The
failure it prevents is also self-erasing: `kickstart -k` kills pogod's entire
process tree, so a descendant that reached it would take down the fleet and
itself mid-deploy, with nothing left running to report why.

`assert_out_of_band` runs on `cmd_redeploy`'s first line, ahead of every other
precondition, and reads two independent signals: a `ps` walk up the parent chain
(matching pogod by basename, depth-capped so a cyclic process table cannot hang
the deploy), and the `POGO_AGENT_*` marker pogod stamps on the agents it spawns.
The second is load-bearing where the first goes blind — a detached agent whose
intermediate parent has exited is reparented to launchd, and an ancestry walk
from there sees no pogod while the caller is still, in every way that matters,
inside the fleet. There is no override flag: one would reduce the guard to the
prose it replaces.

The refusal names the remedy rather than only refusing. An agent that reads a
bare "refused" concludes the deploy is broken and either escalates wrongly or
hunts for a way around the guard, so the message says the redeploy is legitimate
and the CALLER is not, names who may run it (Daniel's terminal, a launchd
one-shot outside pogod's job), and hands the agent a next action — mail `human`
with the revision pogod is owed. `pogo-self-deploy check` is deliberately left
unguarded: it never acts, and it is how the fleet observes its own drift.

Both arms are proved without invoking the deploy — the specimen this guard
refuses is a pogod descendant, and so is the test runner, so a test-by-invocation
that failed would kill the fleet and the tester together. `ps_parent` is the
single seam onto the process table; the unit tests replace it with synthetic
trees and exercise the walk and the refusal as function calls. Live measurement
confirmed the seam itself in both directions: from a polecat the walk resolves
the real pogod, and a launchd-submitted one-shot (ppid 1, no agent marker) passes
the check silently.
