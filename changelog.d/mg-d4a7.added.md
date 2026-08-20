- **A delivered fire always carried its DUE TIME — what it never carried was the
  instruction to compare that due time against the CURRENT clock, and `fired=`
  sits right beside it looking exactly like "when you got this" (mg-d4a7).**
  It is not. `fired=` is when the bytes were SENT. A nudge typed into a busy PTY,
  or a mailbox copy written for an absent agent, is consumed by a turn that can
  run arbitrarily later — so `due ≈ fired` says nothing about whether the reader
  is on time.

  **mg-d4a7's premise was that the due time was missing; it was not.** `due=` has
  been in `[scheduler id=… due=… fired=… ack=…]` since the scheduler's first
  commit (mg-bcfa), and it holds the ORIGINAL due time because `Tick` fires off
  `Entry.NextFire` and advances it only afterwards. The ticket's proposed shape
  — "carry the scheduled due time on the delivered fire" — was already shipped.
  The gap is one step further in.

  **What that cost, measured.** `deploy-verify-architect`'s 2026-08-19 fire:
  `original_due` 03:33:00, `fired_at` 03:33:10 — ten seconds, punctual by every
  lateness measure in this repo, `ackwatch`'s `FireEvent.Late()` (which is
  `fired − due`) included. It was delivered `nudge_unconfirmed` into a stale
  architect and redeemed at 07:52:35, `latency_ms` 15,565,050: **4h19m**. Several
  of that procedure's reads are answerable only inside the 03:00–03:35 deploy
  window — a stamp is the deploy's only if its mtime falls inside it — so at
  06:52 they could not tell the deploy's write from a leftover. Architect's own
  assessment of the resulting report: *"mostly correct with a small unmarked
  wrong region, which is worse than wholly wrong"*. A wholly wrong report gets
  discarded; a mostly-right one gets believed.

  **What every fire now carries**, between the footer and the ack command:

      How late am I: compare due=<t> against the CURRENT clock — NOT against
      fired=, which is when these bytes were sent, not when you are reading them
      (measured gap between sent and read: 4h19m). Lateness is graded: if any of
      this work's reads depend on WHEN they run, mark those stale and answer the
      rest normally.

  **It informs; it does not refuse, and that is the design point.** Late is
  GRADED. Sections reading artifacts that carry their own timestamps are as good
  at 07:00 as at 03:33; only the live-state reads — a stamp mtime, `dig`,
  `pgrep`, a daemon's uptime — go stale. The honest late report is "most of this
  stands, these three lines are REFUSED", which is neither a clean report nor a
  discarded one. A refusal gate at the entrance throws away a run that was mostly
  still valid, so the judgement stays with the procedure, which alone knows which
  of its own reads are time-sensitive.

  **Unconditional, for the reason the refusal is not.** No per-schedule policy can
  know which work is window-bound, and a schedule whose work becomes window-bound
  later would never be re-registered to say so. A mail-check pays one line of
  noise; the alternative leaves the class open for every window-bound schedule
  nobody patched by hand.

  **The eight shipped prompt templates said the false thing outright** — "when
  `due` ≈ `fired` it's an on-time fire" — which is exactly the inference the
  08-19 run made. That sentence is now corrected in place in every one of them,
  with the measurement, rather than only in the mechanism.
