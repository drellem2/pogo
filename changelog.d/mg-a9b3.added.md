- **A memory store that has stopped having a reader is now detectable —
  `pogo check-memdirs` (mg-a9b3).** `pogo doctor` already judges every
  auto-memory index three ways: over the load cap, holding notes the index does
  not name, and carrying a hook whose item has moved on. All three are properties
  of a store some session is still using. None of them can report the store that
  quietly stopped being anybody's.

  **How that happens, measured rather than theorised.** The store is keyed on the
  session's *project*, and for a directory inside a git repo the project is the
  repo, not the directory. So making a parent directory a git repo re-keys every
  agent underneath it onto one shared store. `~/.pogo` became a git repo on
  2026-07-07 and five per-agent stores holding **153 notes** stopped participating
  in recall that day. Nothing was misconfigured, no write ever failed, and every
  file stayed on disk and readable.

  **It is invisible from inside every session, which is what makes it a check and
  not a chore.** The agents that used to write there have healthy recall against a
  different store and indexes at exact parity, so nothing is broken anywhere an
  instrument is pointed. It took five weeks and a duplicated investigation to
  notice: one stranded note had recorded a finding two agents later re-derived
  from scratch and filed as new work (mg-b6bd, then this item).

  **Not an age check.** "Nothing has written here in N days" fires on every
  legitimately dormant per-repo store and still cannot see a store stranded five
  minutes ago. The signal is that the store is keyed to a directory *pogo itself
  owns* — pogo creates the agent working directory and runs the agent in it, so
  such a store has exactly one possible reader by construction, while a shared
  store has many.

  **An empty store is not a finding**, by design: a retired store is left holding
  its index and no notes so the tombstone survives for the next reader, and
  deleting the directory would only mean the next session on that project root
  re-creates it silently. That exclusion is what lets the remedy converge to
  green.

  Read-only, like the rest of the `check-*` family — it never moves or deletes a
  note, because the remedy is a triage and a sweep cannot do a triage. Exit 0
  clean (printing how many store paths were probed), 1 finding, 2 usage,
  **3 measured nothing**: the workdir→store path is constructed from the
  harness's own encoding, which pogo does not own, so a changed encoding yields
  probes that match nothing — and a check whose model has silently stopped
  matching must not report an all-clear it has not earned.

  **A measurement note this work turned up, sharper than the bug it came from.**
  The corpus index this check's population came from is governed by a CHARACTER
  cap, and the obvious way to measure it is wrong twice over. `wc -c` counts
  bytes, and an index of ~200 lines each carrying an em dash overstates by ~400
  — enough to manufacture an overage that is not there, which happened during
  this work and was retracted. The non-obvious half: **this box has no locale
  set** (`LC_ALL` and `LANG` are both empty), so a bare `wc -m` silently counts
  bytes too and agrees with `wc -c` exactly. The reach for the fix lands on the
  same wrong answer with no error anywhere. Any character-count check here must
  set `LC_ALL` explicitly — `LC_ALL=en_US.UTF-8 wc -m` — or it measures the wrong
  unit while looking correct.
