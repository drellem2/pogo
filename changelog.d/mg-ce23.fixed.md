- **`pogo status` bounds its own work-item section, and stops emitting ANSI
  into output nothing is going to display (mg-ce23).** The dashboard printed
  `mg list` verbatim: every item, every title in full, one line each, with mg's
  tag and assignee styling still in it. On this host that was **102,898 bytes**
  for one invocation, of which 96% was the work-item block. Every agent that
  ran the command paid it in context, and the coordinator runs it as routine
  coordination.

  Measured against the same 351-item backlog in the same minute — the old
  rendering is `mg list` with two spaces in front of each line, so both sides
  are the same listing:

      work-item section    before            after
      bytes                100,915           3,289     (-97%)
      lines                    356              38
      longest line             415 columns     103 columns
      lines carrying ANSI      346               0

  The part that matters is that the new number does not depend on the backlog:
  each status group prints its count and at most its first 10 items, each line
  cut to 100 columns, followed by a line naming how many were elided.

      === Work Items ===
        351 items: 100 available, 6 claimed, 3 pending, 242 done
        available:
          mg-01f7    task     DANIEL DECISION, not a wait: settling 'is the launchd…
          …
          … 90 more (pogo status --full)

  **`--full` turns both bounds off** — every item, every title — so nothing is
  reachable only through another command, and `--json` is unbounded
  unconditionally: a machine consumer asked for the data, not for a dashboard.

  Two things this was NOT fixed by, deliberately:

  - *Not by shortening titles.* This fleet's titles carry the finding, which is
    why the block was 21KB in early August and 102KB a week later with nothing
    but the item count having changed. A consumer with terse titles would never
    have noticed — but a summary command that imposes no bound of its own is
    wrong independently of who is using it, and it was the only thing between
    `status` and unbounded growth.
  - *Not by dropping the listing.* The counts line is what makes the elision
    honest. A reader who sees ten items under `available:` now learns there are
    100, which the unbounded listing never said either — it had to be counted
    by hand.

  **The ANSI leak is separate and affected everyone.** mg styles tags and
  assignees whether or not anything is going to render them, so piped and
  captured `pogo status` carried literal `[2m` sequences into files,
  transcripts and agent context. Escapes are now kept when stdout is a terminal
  and stripped otherwise, and stripped from `--json` unconditionally — there
  they style nothing a parser can see and cost tokens to every agent reading
  the output. Stripping runs *after* the `--assignee` filter, which recovers
  the assignee from exactly those escapes.

  **Three ways the fix could have re-created the defect, checked rather than
  assumed:**

  - *The renderer's own lines are bounded too.* Cutting the item lines while
    printing an unbounded counts line and an unbounded header would be this
    ticket's defect in a smaller font. Every line the section emits goes
    through the same cut, and a test plants a 300-column group header to prove
    it.
  - *The cut counts runes, not bytes, and closes what it opened.* mg's titles
    are full of em-dashes; a byte cut lands mid-rune and prints a replacement
    character. A cut inside a styled run now emits a reset, which a terminal
    otherwise inherits for everything printed after the frame.
  - *The elided count is over the whole group, not over what survived.* An
    elision line reporting the size of its own output would be worse than no
    line at all.

  Out of scope and still unbounded: the refinery queue's per-MR progress lines
  run to ~400 columns. They are bounded by the queue rather than by the
  backlog, so they are a fixed cost, not a growing one.
