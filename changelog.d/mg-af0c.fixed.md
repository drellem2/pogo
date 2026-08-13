- **The doctor's `refinery history` advice names the window it reads — the last
  consumer that answered "were there failures?" from a destructively-pruned
  18-hour window (mg-af0c).** `prompts/crew/doctor.md` told the doctor to "Check
  `pogo refinery history` for error details" and labelled the command "Completed
  merges" in its CLI reference, both unbounded. mg-e9ee established that
  `pruneHistoryLocked` **deletes** entries past `MaxHistoryLen` (100) /
  `MaxHistoryAge` (7d) and persists the deletion, and that the count cap bites
  first at any real merge rate. Re-measured 2026-08-13: the retained window was
  100 rows spanning **18h28m** (2026-08-12T07:16 → 2026-08-13T01:44) against
  **926** merge requests over 30 days in the event log — a 9× window, and the
  log reaches back to 2026-04-25.

  The doctor is the worse place for this defect than the coordinator was. It is
  asked "why did my polecat fail?" — a question about something that *already
  happened*, which is exactly the case the retained window cannot answer. A
  failure from yesterday afternoon is already deleted, and "nothing in history"
  reads as "no failures" rather than as "outside the window".

  Both sites now use `--since=30d`, and the Common Issues entry keeps apart the
  two readings that a consumer copying the recipe loses: **empty output means
  healthy within the window you asked for** — a real answer only because it was
  named — while a **non-zero exit with `TRUNCATED` on stderr is not a healthy
  empty, it is an unknown**. `--since` keeps stdout's shape identical, so this
  is a flag, not a rewrite.

  **The ticket's premise about `mayor.md` was wrong, and is recorded here so it
  is not re-filed.** mg-af0c was filed on the belief that mg-2ca3's step-3 check
  still ran over the pruned window and that mg-e9ee "shipped the capability
  without knowing who consumes `refinery history`". In fact mg-e9ee's own commit
  (3a19646, 2026-07-30) did both halves for `mayor.md` in the same change that
  added the flag: the classifier reads `--since=30d` under `set -o pipefail`,
  and the unqualified "empty output is the expected, healthy answer" was already
  replaced by "healthy *within the window you asked for*" plus the non-zero-exit
  warning — pinned by `TestMayorStep3StatesItsWindow`. The seam the ticket
  described was real; it was one file over.

  Guarded by `TestDoctorRefineryHistoryStatesItsWindow`, which pins both
  corrected sites and bans both unqualified forms. Verified as a positive
  control: against the pre-fix `doctor.md` it produces 10 assertion failures.
  The `--since` precondition the ticket asked to re-check was confirmed against
  the *running* binary rather than the clock — `pogo refinery history --help`
  documents the flag and `--since=30d` exits 0 — and the flag is resolved
  CLI-side from `~/.pogo/events.log`, so it does not depend on the daemon's
  revision.
