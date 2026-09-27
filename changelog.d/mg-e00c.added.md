- **wake-watch: pogod now wakes an agent when there is something for it to do
  (mg-e00c, phase 1 of mg-5496, SHADOW).** A new pogod component tails
  `~/.macguffin/events.jsonl`. On `mail.sent` to a running agent (by its name or
  by the work-item box of the item it holds), or on an assignment
  (`work.edited fields=assignee`, including `blocked:<agent>`, and `work.created`
  with an assignee, read from the item because the event does not carry it), it
  types ONE pointer into the agent's terminal. The pointer is at most 100 bytes,
  enforced in code, with the command last (`mail from mayor: "…" — mg mail list
  e00c`). A recipient gets at most one pointer per 60s, which carries a count.
  Unread mail and unclaimed assignments older than 15 min are re-pointed once per
  15 min up to 3 times, then reported (`wake_unconsumed` plus one mail to the
  coordinator). Mail to an agent that is not running is bounced to the sender and
  the coordinator. The offset, re-nudge budget and seen-agent names persist in
  `$POGO_HOME/wakewatch/state.json`, so a restart does not re-point old mail. A
  missing `events.jsonl` is BLIND (recovery suspended), never "no mail". Events:
  `wake_pointer_sent`, `wake_renudge`, `wake_unconsumed`, `wake_bounce`,
  `wake_pointer_skipped`, `wake_watch_armed/blind/sighted`. Configured under
  `[wake_watch]`; ACTS. **The mail-check timers stay on.** `pogo
  check-wakewatch` is phase 2's gate: it joins every timer-driven mail read
  against the pointers sent before it and lists the misses (exit 1 on a miss, 3
  when blind).
- **`pogo nudge` to an agent that is not running now FAILS and names its
  mailbox (mg-e00c).** It used to fall back to `gt mail send`, a system nothing in
  this fleet reads, and print "sent via mail". `client.NudgeOrMail` and
  `client.SendMail` are gone; use `client.NudgeRunning`.
