- **`pogo doctor --check` stops crying stale-claim on every clean macguffin
  store — the alarm fired exactly when nothing was wrong (mg-b13b).**
  The macguffin line reported `! macguffin (mg)  1 claimed work item(s) — check
  for stale claims` against a store containing no work items at all:

      $ MG_ROOT=$empty mg list --status=claimed
      No claimed work items.
      $ MG_ROOT=$empty pogo doctor --check | grep macguffin
      !  macguffin (mg)        1 claimed work item(s) — check for stale claims

  **The mechanism was a sentence being counted as an item.** The count was the
  line count of the *rendered* listing:

      items := strings.TrimSpace(string(mgOut))   // mg list --status=claimed
      count := len(strings.Split(items, "\n"))

  `mg list` is prose, and its empty-store answer is one non-empty line of it —
  `No claimed work items.` — so a store with nothing in it counted as one.

  **This is why it never read as an off-by-one.** Against the real root, with
  five genuinely claimed items, the check said five and was right; only the empty
  store had a sentence to miscount. The filing ticket measured that and explicitly
  declined to call it arithmetic, which was the correct call: the two regimes have
  different causes, and a `count - 1` would have broken the one that worked.

  **The count now comes from `--json`.** NDJSON, one object per item, and no
  bytes at all for an empty store — there is no sentence to miscount. Two
  properties come with the switch rather than being asserted: the parse is a
  decode, so a notice or banner mg grows later is a visible error here instead of
  a silent `+1`; and an item is an object *carrying an id*, so mg's error
  envelope (`{"error":{…}}`) can never be counted as claimed work. `Output()`
  replaces `CombinedOutput()`, since stderr merged into the stream would be a
  parse failure at best and a phantom item at worst.

  **A third state was being reported as the clean one, and now says so.** When mg
  is installed but cannot list — an uninitialised `MG_ROOT` is the ordinary cause
  — the check printed a bare `✓ macguffin (mg) installed`, indistinguishable from
  a store it had read and found clean. That is the same defect pointed the other
  way: a detector that quietly stopped running looked exactly like one with
  nothing to report. It now prints `installed — claimed items NOT checked:` and
  mg's own reason, unwrapped from the JSON error envelope where `--json` puts it
  (exec's `exit status 1` carries none of it).

  **All four regimes from the ticket, re-measured against the built binary:**

      regime                                    before   after   mg says
      empty store                               warn 1   pass 0   0
      empty store + 3 files mg does not parse   warn 1   pass 0   0
      real root, 5 genuinely claimed            warn 5   warn 5   5
      uninitialised root                        "installed"  "installed — NOT checked: reading claimed/: …"

  **Why this was worth a ticket rather than a one-character change.** The false
  positive fired precisely when nothing was wrong, on the one line a real stale
  claim surfaces on — an item held by a dead pid is invisible to dispatch and
  appears nowhere else in the checklist. A detector that shouts on healthy input
  trains its readers to skip it, which is how the loud ones stop being read.

  **The regression arms were run against the pre-fix code, not just the fixed
  one.** `TestDoctorCheck_CleanStoreReportsNoStaleClaims` and
  `TestDoctorCheck_UnreadableStoreSaysSo` both fail on `main`, reproducing the
  reported string verbatim (`warn  1 claimed work item(s) — check for stale
  claims`) — a test that has never failed is a claim, not a control. Coverage is
  three-layered on purpose: the parse in isolation (including the byte-for-byte
  `No claimed work items.` fixture, which must now be an *error* rather than a
  count); the real binary against a stub `mg` that answers both stream shapes,
  because the defect lived in the wiring and no unit test could see it; and the
  real binary against the **real** `mg`, building a store and claiming items in
  it, because the stub encodes a contract about mg that would otherwise keep
  passing after mg changed.
