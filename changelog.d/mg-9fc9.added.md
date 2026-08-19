- **After N consecutive nights lost at the transport step, the nightly bounces
  the fleet anyway — a restart needs no remote (mg-9fc9).** The nightly deploy is
  this box's only *automatic* recovery path: it restarts the fleet, and a restart
  is what clears a wedged agent. On any of the five nights 2026-08-15..19 it would
  have ended a 118-hour blackout. It could not, because it needs the same network
  the fault had taken out — `ssh: Could not resolve hostname github.com`, 30
  retries over 7980s, rc=10, every night. Nothing misbehaved: every retry tier
  fired, the classifier refused to name a cause the transport never let it
  establish, and both mayor and human were paged. **The recovery mechanism shares
  a dependency with the failure it recovers from**, which is a property of the
  topology rather than a defect in a part. Recovery, when it came, came from
  outside the system entirely — Daniel typed a message.

  **`pogo-self-deploy bounce`** is the half of a deploy that needs no remote: no
  fetch, no build, no `do_prove`. It keeps the drain gate (now one shared
  function, called by both subcommands rather than copied), the out-of-band
  ancestry guard, and both post-restart verifies. It delivers no new code — that
  genuinely needs the remote — and it ends a blackout the agents were only in
  because nothing had restarted them.

  **`scripts/launchd/pogo-deploy.sh` decides when to reach for it.** After
  `POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER` (default **2**) consecutive nights lost at
  the TRANSPORT step, and only once every fire of the night is spent, it runs the
  bounce and mails the result. Four things about that are deliberate:

  - **Keyed on the transport, not on "the deploy failed".** A night that failed
    because the *tree* is bad has a different remedy, and bouncing over it would
    be destructive noise. `dirty`, `diverged` and `checkout` are all read after a
    successful fetch, so they *clear* the streak; `config` fails before any
    network call and leaves it alone. Three answers, because two would have had to
    guess about one of them.
  - **Nights, not fires.** The count is idempotent per date, so three fires of one
    bad night cannot manufacture a threshold of two.
  - **It announces itself locally.** A mail to `mayor` and `human` out of the
    maildir, plus a `deploy_transport_fallback` event — on the night this fires,
    the network is what is broken, so an announcement that needed it would be the
    same defect rebuilt inside the remedy.
  - **The drain still rules.** `--force` is *refused* by `bounce`, not merely left
    unset. A polecat holding commits that exist only in its worktree stops the
    bounce, and that refusal is reported rather than overridden.

  The fallback also gets its own window reserve (`POGO_DEPLOY_BOUNCE_RESERVE`,
  300s) instead of the deploy's 1200s. That is not a tuning detail: the mg-5515
  vigil probes until the *deploy's* budget hits zero, so charging the bounce the
  deploy's reserve would have left it no window on precisely the nights it exists
  for — the remedy disabled by the patience that discovered the outage.

  Two couplings survive inside the fix and are named rather than quietly
  accepted. The fallback fires in exactly the state that makes a **drain refusal**
  more likely — a fleet that has gone N nights without a restart is likelier to
  hold a wedged polecat, and a wedged polecat that committed without pushing is
  what the drain refuses to orphan — so the refusal is mailed, names the holders,
  and says the fleet is still owed a restart. And the fallback lives **inside the
  nightly job**, so a fault that stops the job firing at all takes the fallback
  with it; that is a different fault with a different detector
  (`internal/staleness/nofire.go`), and it is mg-f867's shape one level up.

  The general form, which is the half worth carrying to mg-f867 and mg-3a8a: **a
  remedy must not depend on the resource whose loss it remedies**, and where some
  dependency is unavoidable, the remedy needs a second path that does not — with
  the reachability of that path under the fault demonstrated rather than assumed.
  Every dependency of this fallback was enumerated against that rule and each is
  either severed (the network, the checkout, the deploy's window reserve) or
  named above as a residual.
