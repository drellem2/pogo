The Claude trust-dialog hook no longer abandons a late-rendering dialog at a
fixed 8s. `TrustDialogTimeout` was an independent wall-clock guess that started
at spawn and gave up 8 seconds later, so on a CPU-starved host under concurrent
spawns the "Quick safety check" dialog could render *after* the hook had
returned — nothing then dismissed it, the composer never appeared, and the
polecat hung until a human answered it (drellem2/macguffin#25). The bound is now
sourced from `DefaultNudgeProfile.InitialNudgeTimeout`, the same cold-start
budget the spawn path already waits for the composer, so the hook that unblocks
the composer cannot stop watching first. Watching longer is close to free: a new
composer-ready guard resolves the hook early on every healthy spawn and also
prevents the widened window from mistaking Claude's echoed kickoff prompt for
the dialog and pressing Enter into a live composer. A real-PTY race test drives
the loop on a sub-second budget and pins both the defect (positive control) and
the fix. This is deterministic prevention at the hook, complementing the
probabilistic auto_renudge recovery net (mg-c33e).
