package main

// scheduleLong is `pogo schedule`'s help text.
//
// A package-level const, not a literal inside the cobra declaration, so a test
// can read it (see schedulecompletionhelp.go for the same reasoning). The text
// listed three things a schedule survives and said nothing about agent
// lifecycle. A reader could not tell that a mail-check row is dropped when its
// agent exits and pogod will not bring it back (drellem2/pogo#205). The
// lifecycle paragraph is pinned by schedulehelp_test.go. It must not promise
// survival across an unsupervised exit.
const scheduleLong = `Register a recurring or one-shot wakeup with pogod.

Recurring (--cron required):

  pogo schedule crew-research --cron "*/15 * * * *" --id research-poll \
    --message "check the queue"

One-shot (--once + --in):

  pogo schedule cat-foo --once --in 30m --message "wake up"

Schedules persist in ~/.pogo/schedules.json and fire from pogod's heartbeat
loop — they survive host sleep, NTP steps, and pogod restarts (unlike Claude's
in-process CronCreate). The default replay policy is "once": after a long sleep
the schedule fires exactly once and reschedules to the next future occurrence.

Agent lifecycle: a mail-check-* schedule lives only while pogod supervises its
agent. It is NOT scoped to the seat. A supervised respawn (restart_on_crash=true)
keeps the row, and park/wake removes it and puts it back. An exit pogod will not
respawn removes it with reason=agent_gone. That covers restart_on_crash=false, and
a respawn suppressed by the synthetic-failure detector. Schedules of any other
kind are never removed by an agent's exit.

The durable fix is for an agent to re-register its schedules at startup,
every time. That includes a seat restarted outside pogod. Pass --id: a second
registration with the same (agent, id) replaces the first and does not add a
duplicate, so re-registering is safe.`
