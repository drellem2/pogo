package main

// scheduleCompletionLong is `pogo schedule completion`'s help text.
//
// A package-level const rather than a literal inside the cobra declaration,
// for the reason this file's header already gives about the column heading:
// prose living inside a Run/command literal is prose no test can read, and
// "not testable" is how the old COMPLETED heading survived three tickets that
// each diagnosed it correctly.
//
// It was the right lesson and it had not been applied here. This text asserted
// that "the number that separates a busy agent from a dead one is the unacked
// streak" — false, and false in exactly the direction that costs an
// investigation: on 2026-08-19 the mayor read a 6-streak on the schedule
// guarding the deploy drain and could not tell absence from neglect, which is
// what mg-7837 was filed to settle. The claim is corrected, and now a test
// reads it.
const scheduleCompletionLong = `Report how many delivered fires were actually acknowledged as complete.

This is the query the 2026-07-22 events log could not answer. Schedules that
have never acked are counted as UNKNOWN, not failing — only a schedule that has
proven it can ack, and then stopped, is evidence of anything.

"Never acked" spans re-registrations. A schedule re-registered at agent boot
keeps its tracked status but restarts its counters, and is reported separately
so a thin denominator is visible rather than inferred.

The shape to watch for is fleet-wide: one agent skipping one ack is noise,
every tracked schedule going to zero within the same minute is an outage.

WHAT THE RATIO IS NOT (mg-a14c). It is not the fraction of scheduled work that
got done, and 100% is not available. Only the newest fire's token is redeemable,
so a run of fires landing inside one agent turn yields at most one ack however
completely the work was done. The ratio is exactly the reciprocal of the mean
attention gap — a TURN LENGTH in cadence periods — which is why this command
prints it both ways. An alarm built on the percentage measures how long turns
are; the one that does not saturate is the unacked streak.

THE STREAK DOES NOT SEPARATE A BUSY AGENT FROM A DEAD ONE (mg-7837). That was
claimed here and it is false: a fire delivered into an agent that no longer
exists and a fire dropped by an agent that is turning both leave the streak
climbing without bound, and this counter holds nothing that tells them apart. On
2026-08-19 the mayor read a 6-streak on its own predeploy quiesce and had to
file a ticket to find out which; all six fires had landed inside the
2026-08-14..19 window in which every crew turnlog is empty. "pogo schedule list"
now qualifies each streak with what the agent's turnlog says about the newest
fire, and "pogo check-turns" is the direct read.`
