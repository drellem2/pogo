- **A running gate's OUTPUT TEXT is now readable, not just its volume
  (mg-9adc).** `pogo refinery show <id>` printed progress metadata about a gate
  in flight — how many lines, how long ago, at what CPU — and `--json` carried
  `last_output`, which is a *timestamp*. A reader who finds that field in a JSON
  payload reasonably expects the last output, gets a time, and concludes the
  gate is emitting nothing, which is the opposite of true for a gate printing
  steadily into a file nobody exposes. The heartbeat machinery already read that
  stream to count lines and threw the content away.

      --- Gate output so far ---
      312 lines so far, of which 285 lines BETWEEN the first 25 and the last 2
      are NOT shown (bounds: head 25, tail 40)
        1 | watchlist consistent: 17 paths; import closure 10 modules; datasets read 1
        2 | === watched paths changed:
        3 |     .github/workflows/script-controls.yml
          ~ 285 lines not shown here ~
      311 | ok  internal/refinery  12.400s
      312 | ok  internal/hostload  0.310s

  New `StepProgress.OutputExcerpt`, in `--json` under `output_excerpt` — a new
  field, deliberately not a re-typing of `last_output`, which stays a timestamp
  where consumers already expect it.

  **Why volume was not enough.** Every signal the record already carried
  measures rate or amount. A gate frozen at `140 lines, last 26m ago` reads
  identically whether those lines end in a passing suite or in `Building pogod
  into the sandbox...`. A line count cannot distinguish a compute phase from a
  hang, and one evening a gate ran 1h17m with its stdout frozen at exactly that
  reading; diagnosing it meant reading the raw process tree instead, which was
  got wrong twice — once by a self-matching grep, once by sampling a parent
  process at 0% CPU while its child burned 543%.

  **The measured cost is not delay, it is wrong explanations.** A specific,
  testable hypothesis about why that gate was slow travelled doctor → pm-pogo →
  pm-onethird and cost a peer product two tickets filed on a false premise. The
  gate's **first line** — `watchlist consistent: 17 paths` — refuted it outright,
  and was unreadable for the entire window. Nobody read carelessly; every party
  labelled it a hypothesis and named the test. As pm-onethird put it: a window in
  which the evidence is dark *selects for stories*, because a hypothesis that
  cannot be killed accumulates relays instead of tests and each relay reads as
  corroboration. Three agents agreeing was one unread file counted three times.

  **A tail would not have fixed it, so this is not a tail.** "Show me the last N
  lines" is what everyone asks for and it would have left the defect fully
  intact: the refuting line is the *first* one the gate emits, and by minute 77
  it is far outside any reasonable tail. Gates state what they resolved and what
  they are about to run in a header. So the first **25** lines are captured once
  and never evicted, alongside the rolling last **40**.

  **The bound is stated wherever it bit.** Gate output can be large and a gate
  may be mid-write, and a bounded read that manufactures an absence is its own
  defect — this arc has already lost a day to a `head -40` on an 81-line mail
  producing a confident wrong diagnosis. So: the header states the total, the
  elided count and the limits; the gap prints *as* a gap; kept lines carry the
  gate's own line numbers, so head-then-tail cannot read as a contiguous
  transcript; and a line longer than 500 bytes is cut with the cut marked **in
  the line**, because the reader who needs to know a line was truncated is the
  reader looking at that line. A complete excerpt says `all shown — nothing
  elided`, which is a different and much stronger claim than saying nothing.

  **Silence and absence are different claims and render differently.** A gate
  that has said nothing reports `NOTHING YET — ... This is a measurement, not a
  bound hiding one`. A record from a pogod that never captured gate text reports
  `NOT RECORDED — ... That is not the same as the gate being silent`. They lead
  to opposite actions. An **unterminated** line — the gate caught mid-write — is
  reported separately and labelled `(still being written — no newline after it
  yet)`: that is exactly the reading a gate halted mid-phase leaves behind, and
  it is what a polecat never saw across three 60-minute gate timeouts spent
  believing its own branch was at fault.

  `pogo refinery queue` gains `said first:` / `said latest:` beside its line
  count, and names the command that prints the full bounded excerpt — a two-line
  summary that does not say where the rest is reads as the whole of it. The
  pogod heartbeat log gains `gate_last_line=`.

  **Positive control.** `TestRunningGateTextIsReadableWhileItStillRuns` starts a
  real gate that prints a known sentinel and then works, and requires the text to
  be readable from the persisted record **while `EndTime` is still zero**. A test
  that read the output after completion would have passed against the old
  behaviour and proved nothing — that gate text exists once the merge resolves
  was never in question. Bounding, elision accounting, the line bound, the
  mid-write line, CRLF, chunk-split lines and the state round trip each carry
  their own test.

  **Cost.** The excerpt is bounded at 65 lines and 500 bytes per line, so a gate
  emitting a million lines or one 4 GB line costs a fixed ~30 KB. It is captured
  on the gate's write path under its own small mutex — never the refinery's lock,
  which would make the pipeline pay for its own observability.
