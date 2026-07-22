# synthfail test fixtures

## Real, verbatim

`auth-expired-2026-07-22.jsonl`, `rate-limit.jsonl`, `weekly-limit.jsonl` and
`spend-limit.jsonl` are **unmodified lines copied out of this machine's Claude
Code session transcripts**, one contiguous run per file. The auth fixture is
from the 2026-07-22 fleet outage window itself (mg-18d0); the other three are
the more frequent members of the same class — mg-18d0 counted rate-limit at
2818 occurrences against login-expired's 914, which is why the detector is built
for the class and not for auth.

They carry no user content: a synthetic failure turn is written entirely by the
harness and its only free text is the harness's own error string.

## Structural, hand-built

`wedged-silent.jsonl` is the negative control: an agent that was working and
then **stopped writing**. Its turns are structurally real — real model ids, real
non-zero token usage, the record shape the harness actually emits — but the
prose is invented, because a genuinely wedged agent's last turns are its own
work and do not belong in a repo.

This fixture is the load-bearing one. A detector verified only against the
failing case cannot distinguish the two failure modes, and distinguishing them
is its entire job: a wedged agent writes NOTHING and must still be reported as
wedged (restart applies), while a synthetically-failing agent writes a new turn
on every nudge and must never be restarted.

`healthy-then-failing.jsonl` is the transition: real work turns followed by the
class. It proves the presence of legitimate history does not mask a live
failure, and that the reader does not simply key on "the file has an error in it
somewhere".

## Live verification

The checked-in fixtures are what CI runs. `TestScan_LiveIncidentTranscripts` in
`live_test.go` additionally verifies against the untouched originals still on
this machine — `pm-pogo` FIRING across the 2026-07-22 window and `doctor`, which
received no nudges that day and therefore emitted nothing, STAYING SILENT over
the identical window. It skips when those paths are absent, which is every
machine but this one.
