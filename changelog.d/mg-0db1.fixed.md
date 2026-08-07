- **`pogo check-mailloops` names WHO it did not judge on every render branch,
  and the JSON carries the same set with a reason (mg-0db1,
  Refs drellem2/pogo#127).** On the reporting host the command judged 2 of 6
  agents and printed `All 2 judged agent(s) have a mail-check schedule. (6 in
  the registry.)` at exit 0. Four of six were not judged; the output named
  neither which four nor why, and `--json` carried only `scanned` and `judged`.
  Reproduced live during triage at 5 of 12.

  **This was an inconsistency inside one command, not a missing feature.**
  `MailLoopReport.Render` had three branches and only the GREEN one omitted the
  who-was-not-judged disclosure: the `Judged == 0` branch states it in full, and
  the RED branch carried half of it (`Judged N of S`, a count with no way to
  turn it into names). Three in-tree precedents already do this correctly —
  `ackwatch.Report.renderCoverage`, `deafwatch.renderBody`, and the shipped
  `unjudged` JSON field in `internal/staleness/prompts.go` — so the fix applies
  an established convention rather than inventing one.

  All three branches now end in a single `renderCoverage`, which is the point:
  the smaller diff would have been to patch the green branch, and that leaves
  three branches free to diverge again. The RED branch's bare count is fixed as
  a side effect.

  **The unjudged set rides on the report, so the JSON says what the text says.**
  Each entry carries name, type, and a machine-stable reason from the coarse set
  `polecat` / `not_running` / `not_configured`. That set is deliberately coarser
  than the three categories `--help` lists: `IsConfiguredAgent` returns false
  BOTH for an unreadable prompt tree and for a genuinely unconfigured agent, so
  the finer taxonomy is not computable today, and emitting a reason the code
  cannot back would be this issue's own failure mode one level in. The `--help`
  text now says so. The collapse itself is a separate defect, filed separately.
  The reason and the judgeability predicate are one function
  (`mailLoopExclusionFor`, which `mailLoopJudgeable` is now defined in terms of),
  so the roster cannot name a reason the predicate would not give.

  **Absent and empty are distinguishable on the wire, and absent renders
  UNKNOWN — never zero.** `internal/client` plain-decodes this struct with no
  version negotiation, so a pogod older than the client simply does not send the
  new field. A plain slice would have flattened that into a confident "0 not
  judged" — this issue's exact defect, green, inside its own fix, on the fleet
  that filed it. Not hypothetical: the running pogod was ~93 commits behind main
  when this was written, so the skew case was the CURRENT state. `Unjudged` is
  therefore a pointer with no `omitempty`: a report that judged everything puts
  `"unjudged": []` on the wire, and an absent field renders the count (derivable
  from `scanned - judged`, which every version sends) with WHO and WHY stated as
  unknown. `TestMailLoopReport_AbsentUnjudgedSetRendersUnknownNotZero` decodes a
  payload with the field absent and asserts the render does not claim full
  coverage.

  **Exit status does not move.** `Actionable() = len(Missing) > 0` is unchanged.
  Everything in the unjudged set is excluded on purpose — mg-738f drew that
  boundary and the cry-wolf guarantee rests on it — so firing on it would make
  the exit status useless. This changed what the command DISCLOSES, not what it
  judges; recorded here and at the predicate rather than left as a silence.
