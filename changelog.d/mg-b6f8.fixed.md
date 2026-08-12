- **Every stall-watch notification arrived under the same subject line, so
  repeats could not be told from new alarms (mg-b6f8).** All five stall-watch
  categories delivered their mail under one constant string,
  `stall-watch: work piling up`, composed in `cmd/pogod` at the delivery site —
  where the category, the item count and the item ids are all out of scope. The
  message body had always carried them; the subject discarded them.

  Measured on this box over 2026-08-11 12:00Z .. 2026-08-12 09:52Z, `human`
  received 18 stall-watch mails. Every one was a blocked-reminder, and their
  bodies covered **three different item sets** — `mg-fbc1` alone, `mg-8888`
  alone, both together, then `mg-0218` — at counts of one and two. All 18
  subjects were byte-identical. The recipient reads mail through Discord, which
  renders the subject, so eighteen distinguishable facts arrived as one sentence
  printed eighteen times, and no repeat could be told from a new alarm without
  opening it.

  `stallwatch.Nudger` now takes a `Notice` (subject + body) instead of a bare
  message, and each check composes its subject from the facts it already stamps
  into its event: `stall-watch: <head>, oldest <age> — <ids>`. The three parts
  are chosen so any two notices that differ at all render differently — **head**
  separates the categories (`1 item blocked on you` and `1 item unclaimed` mean
  opposite things to the same reader and arrive in the same list), **ids** name
  which items, and **age** is what separates a *repeat*: count and ids are
  identical across the repeats of a persisting stall, and the oldest item's age
  is the only one of the three that must have moved. It is strictly increasing
  for a fixed item set, and minute resolution is finer than the shortest
  cooldown any category uses (3m, the priority wake). The measured sequence
  above now renders as five distinct lines, e.g.
  `stall-watch: 2 items blocked on you, oldest 3h — mg-8888, mg-fbc1` then
  `stall-watch: 1 item blocked on you, oldest 1h30m — mg-0218`.

  **Nothing was suppressed and no interval was lengthened.** The rate limiting
  already worked — 18 notices in 22 hours is far from every occurrence — and
  those notices were correct; several genuine stalls (mg-1c60, mg-a517, mg-253e)
  were dispatched off them overnight. Trading a working signal for quiet would
  be a regression that looks like a fix. The complaint was that repeats were
  *indistinguishable*, not that they were too many, so only the subject changed.

  Three things follow from the fix being a subject generator, i.e. an artifact
  that can exhibit the defect it remedies:

  - Past five ids the list truncates to `+N more`, so two *simultaneous* batches
    sharing a five-id prefix at an equal count would differ only in age. Left in
    and documented rather than engineered around — the fix (a digest of the full
    id list) would cost the subject the readability it exists to buy — and five
    is well past the largest batch stall-watch has actually emitted (two).
  - The delivery site keeps the old constant as a fallback for a notice that
    carries no subject, so an empty subject line is impossible. Seeing it in a
    maildir now means the *watcher* composed nothing, and a test fails if any
    check fires without one.
  - `stall_watch_fired` gains `nudge_subject`, so "which notices were
    indistinguishable in the recipient's notification list" is answerable from
    `events.log` with one `jq | uniq -c` instead of by hand-reading a maildir —
    which is how this complaint went unfiled long enough to be measured twice.

  `ack-watch` was measured in the same window as a control and deliberately left
  alone: 15 mails under 2 subjects, but its subject is *computed* from the fire
  counts, so it repeated because those counts were genuinely stable rather than
  because it discarded them. Its duplication also stopped on its own at 08-11
  19:10 when the blackout cleared, 4h before the ticket was filed. A fix aimed
  at "notification noise" in general would have landed on the half that was
  already over.
