- **Two coordinator lessons that existed only as hand edits to one box's
  installed `~/.pogo/agents/mayor.md` are now in the shipped
  `prompts/mayor.md`.** Neither string was present in the embed at `1ebf2dc`.
  They were learned on this deployment on 2026-08-13 and they were stuck on it:
  every other pogo deployment had neither, and reinstalling the prompt on this
  one would have erased both.

- **`declares-remainder` is now warned about AT DISPATCH**, in the spawn step,
  immediately before the already-working check. The tag makes `mg done` refuse
  without a successor, so a worker that merges without filing one leaves a
  *finished* item sitting `available` and drawing stall-watch notices — and it
  is reaped at merge, so only the coordinator can clear it. The dispatch body is
  a snapshot the worker reads; telling it there is the only channel that reaches
  it in time. Cost twice on 2026-08-13 (mg-6e4f, mg-4020).

- **The completed-worker sweep now says to tell the filer their item landed**,
  as step 2 of the cleanup list after `mg archive --days=0`. pogod mails the
  filer itself on merge and self-close, so this is only the residue: the paths
  where the template forbids the worker to close its own item, of which triage
  is the clearest (drellem2/pogo#144, mg-1d9e).

- **The carve-out was re-verified rather than inherited, and it is sharper than
  it was written.** pogod has exactly two filer-notification call sites
  (`cmd/pogod/reap.go` for `RouteMerge`, `cmd/pogod/donereap.go` for
  `RouteSelfClose`). The self-close notice is emitted by the done-reaper, which
  reaches items only through `Registry.PolecatActivityAt` —
  `internal/agent/polecatactivity.go:55` skips any agent that is not `alive()`.
  So a close the **coordinator** performs after stopping the worker is seen by
  nothing, which is precisely the triage retirement at the human gate: the
  triage template leaves the item `claimed` and the coordinator retires it with
  `--successor` later. The paragraph now says so, and cites the verification.

- **Both paragraphs are pinned on PLACEMENT, not presence.** Placement is what
  each lesson is about — one has to reach the dispatch body, the other the
  completed-worker sweep — and a paragraph relocated to the bottom of the file
  satisfies a `strings.Contains` while teaching nobody anything at the moment
  they need it. `internal/agent/mayorlessons_test.go` bounds each by its
  surrounding section. Shown able to fail, both arms each: removing a paragraph
  fails the presence assertion; appending it verbatim at EOF fails the placement
  and citation assertions.

- **The daemon half of the carve-out is pinned too**, so a change that closes
  the gap fails a test that names the prompt paragraph rather than leaving it
  describing a gap nobody can act on.
  `TestACoordinatorCloseWithNoLiveWorkerTellsNobody` runs the same reaper and
  the same terminal-item probe twice, changing only liveness: no live worker →
  no notice, live worker on the same closed item → one notice. The positive
  control is in the test because the negative alone is satisfied by a reaper
  that notifies nobody ever.

- **This does not by itself stop the box declining `mayor.md` updates, and the
  ticket's premise that it would is wrong.** The install conflict is decided by
  `stamp.BodyHash != currentBodyHash(destPath)` (`internal/agent/prompt.go:1813`)
  — a property of the installed file's own stamp, not a comparison against the
  embed. Measured on this box: the installed stamp claims
  `body=sha256:e20f8f69…` and the file's actual body hashes to `1d335a24…`,
  because the hand merge at 02:07Z on 2026-08-14 edited the body without
  re-stamping. The next shipped `mayor.md` will therefore still divert to
  `mayor.md.dist` until the installed copy is re-stamped or force-installed.
  That is an operator action on the live deployment, not something a merged
  branch performs.
