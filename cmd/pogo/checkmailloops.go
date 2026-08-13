package main

// Help text for `pogo check-mailloops` — the missing-mail-loop reader (mg-032b).
// The command itself is assembled in main.go; the prose lives here so a test can
// hold it against the shape it describes (see checkmailloops_test.go).

// mailLoopsJSONContract states the two wire states of `--json`'s `unjudged`
// field, in the place a MACHINE READER looks.
//
// # Why this is documentation and not a field
//
// drellem2/pogo#127's round-1 review observed that the two renders are not
// equivalent: the text render prints a sentence naming WHO was not judged and
// WHY that is unknown, while a machine reader gets a bare `null` and has to
// already know what it means. The obvious repair — an `unjudged_reason`
// alongside the null — was ruled against in mg-4692, and the reasoning is
// worth keeping next to the text it produced:
//
//   - The JSON is not missing a FACT. The nil branch of renderCoverage prints
//     three things: the count (`Scanned - Judged`, arithmetic over two fields
//     every daemon version sends, so already on the wire), that WHO and WHY are
//     unknown (which is precisely and only what `null` means), and remediation
//     advice for a human. The first two a machine reader already has; the third
//     does not belong in a wire format.
//   - A field whose only job is to explain another field moves the one
//     documented bit (`null` != `[]`) out of the schema and into the payload,
//     where it needs its own documented vocabulary.
//   - The condition is self-extinguishing. `null` occurs only against a pogod
//     older than the client, and this fleet redeploys nightly — a wire-format
//     change is permanent, and paying a permanent shape cost to narrate a
//     nightly-expiring condition is the wrong trade.
//
// What that ruling DOES owe is this block: the contract, stated where a
// consumer reads it, rather than only in a Go doc comment no consumer reads.
//
// Reopen the shape question if a THIRD wire state ever appears — a daemon that
// reports the set partially, or for some agent types only. `null` vs `[]` would
// then no longer be sufficient to carry the distinction. Not on taste.
const mailLoopsJSONContract = `--json: THE "unjudged" FIELD HAS TWO STATES, AND THEY ARE DIFFERENT ANSWERS

  "unjudged": null   this daemon does not report the set — it is older than
                     this client. WHO and WHY are UNKNOWN. This is NOT "nobody
                     was excluded". (An absent key decodes identically; the
                     field carries no omitempty, so a daemon that reports the
                     set always emits it.)
  "unjudged": []     the set is genuinely EMPTY: every scanned agent was
                     judged, and nobody was excluded.

  In BOTH cases "scanned" - "judged" is the honest unjudged COUNT. Those two
  fields are sent by every daemon version, so a consumer never needs "unjudged"
  to get the NUMBER — only to get the names and the reasons. A reader that
  flattens null into an empty list reports "0 not judged" over a fleet it never
  looked at, which is this command's own reported defect (drellem2/pogo#127)
  one level out.

  There is deliberately no field explaining the null; the contract is
  documented rather than serialised (mg-4692).`

// checkMailLoopsLong is the `--help` body for `pogo check-mailloops`.
const checkMailLoopsLong = `Report every agent that has NO mail-check schedule. Such an agent can be
mailed, and nothing will ever wake it to read the mail — it is unreachable by
every coordination path the fleet has, while looking perfectly healthy.

This is the same judgement ` + "`pogo agent diagnose <name>`" + ` has reported as
health=no_mail_loop since mg-de08, asked of every agent at once. That difference
is the point. Until mg-032b the ONLY consumer was that per-agent subcommand,
which takes the agent's NAME as an argument — and not knowing which name to type
is exactly what a silently-unreachable agent looks like from the outside. The
fault was detectable, never announced.

pogod now also announces it on its heartbeat (see [deaf_watch] in
docs/CONFIGURATION.md); this command is for when you want the answer now.

WHO IS NOT JUDGED, deliberately:

  polecats            they register their own loop at spawn (mg-e633) with
                      their own escalation path (mg-6fe0); coverage is the
                      witness, not this.
  stopped agents      a configured agent that is not running is owed nothing.
  not configured      no prompt for it on this machine's prompt tree, which was
                      read: it is not one of ours.
  unreadable prompts  the prompt tree could not be READ, so the agent could not
                      be classified, and a false RED costs more than silence.
                      This one is not a clean exclusion — it is a fault in the
                      instrument, and the report says so in those words.

Those exclusions mean a small "judged" count is normal. Every report NAMES the
agents it did not judge, whatever the verdict — a clean bill of health over 2 of
6 agents is a statement about 2 agents, and it says so. A report that judged
NOTHING says so in as many words rather than printing an all-clear, and a pogod
with no basis to judge at all is an ERROR here, not an empty list.

The machine-readable reason in --json is one of "polecat", "not_running",
"not_configured", "unreadable_prompts" — one per category above. Until mg-7b3f
the last two were a single value, because agent.IsConfiguredAgent returned false
for both and the reason set was deliberately kept no more precise than the code
could back; this text named a distinction the code could not compute, which was
this command's own reported defect one level in.

` + mailLoopsJSONContract + `

Against a pogod older than this client the unjudged set is absent from the wire
entirely. The TEXT render reports that as UNKNOWN — never as zero — because "the
daemon did not say" and "nobody was excluded" are opposite statements. Such a
pogod also still sends "not_configured" for an unreadable prompt tree, and
nothing on the wire distinguishes that from a real one: the report carries no
version, so this client cannot detect the skew and does not pretend to. Read a
"not_configured" from an old daemon as the old collapsed value; "pogo version"
says which you have.

REPORTS ONLY — it never registers a schedule, nudges, or restarts. Re-registering
the loop on the agent's behalf would hide WHY it vanished, and that is the part
worth knowing.

Exit status is 0 when every judged agent has a loop, 1 when any agent is
unreachable.`
