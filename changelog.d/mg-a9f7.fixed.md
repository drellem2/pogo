- **The `--body="` ratchet stops punishing the prose that warns against `--body="`,
  and stops deciding by nesting depth (mg-a9f7).**
  The mg-d91f ratchet's whole design premise is that prose is exempt — its header
  says so at `internal/agent/bodyratchet_test.go`: *"An example line is one inside a
  fenced code block, or indented far enough to be an indented code block. Prose and
  comments are exempt on purpose."* The implementation classified an example line as
  `strings.HasPrefix(line, "    ")`, which is not that property. Four leading spaces
  is where every sentence inside a nested list already sits, so a prose sentence
  containing the literal was counted as *teaching* the hazard purely on account of
  how deeply nested the list it appeared in happened to be.

  Measured on the shipped corpus with the same sentence inserted at each template's
  own continuation column: the old predicate flagged it in
  `templates/polecat-architect.md`, whose steps indent to 5 columns, and passed it in
  `templates/polecat.md`, which indents to 3. **Identical text, opposite verdicts, and
  the only difference was list depth.** Polecat 78d2 hit this during mg-78d2 and
  reworded around it — correct for a polecat in someone else's file, and the reason it
  was found at all, but the next author to document the hazard inside a nested list
  meets the same wall with no reason to expect it.

  **Fixed by implementing the rule a markdown renderer actually uses, not by widening
  an exemption.** An exemption naming prose patterns reproduces the defect with more
  steps — the next sentence phrased differently is punished again — and it would leave
  the header describing a property the code does not have. Two properties now decide
  it, and neither can be moved by nesting depth: indentation is measured **relative to
  the enclosing list item's content column**, so a continuation line is a continuation
  at any depth; and an indented code block **cannot interrupt a paragraph**, so it has
  to open after a blank line. A run-on indented sentence is prose.

  Bare `{{...}}` template-directive lines are skipped rather than treated as markdown.
  This is not cosmetic: `{{if .Branch}}` sits at column 0 in the middle of step 4 of
  `polecat-architect.md`, and letting it close the enclosing list item put every
  following line back at base 0 — re-creating the same bug one paragraph further down
  the file. It did, until it was measured.

  **Both directions are pinned, and on the real corpus.** The threshold was *not*
  raised — raising it to 5 would have been the wrong fix, and the pre-existing test
  that builds its indented example at exactly 4 spaces still passes, which is what
  distinguishes the two. Beyond that: prose is inserted into **all 403 list items** of
  every shipped template at each item's own content column and the tree stays green;
  an unsafe indented example is inserted at those same 403 depths and **403/403 are
  caught**. Either control alone is satisfiable by a broken scanner — one that never
  classifies anything as an indented example passes the exemption half, and the old
  whitespace predicate passed the catching half — so they are only worth anything
  together. Neither test carries an anchor string: the property is about depth, so the
  tests find the depths instead of being told them.

  No prompt content changed. mg-d91f deliberately separated the ratchet from the
  template sweep, and this keeps that separation.
