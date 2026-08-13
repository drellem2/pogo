- **The body-editing block in every prompt that edits a body now says to read
  the title when you append a correction.** The line sits directly under the
  three properties that make `--append-body-file` the safe write, in
  `prompts/mayor.md`, `prompts/pm/pm-template.md` and `prompts/crew/doctor.md`:
  *"When you append a correction, read the title. A correction lands in the
  body; `mg list` shows titles only, and a `done` item's title is what the next
  reader gets. If the title still asserts what you just corrected, retitle in
  the same edit."*

- **It is the second property read the other way round.** The append is safe
  *because* it cannot author the body's leading `# ` heading — and that heading
  is the title, the only place a title is stored. So the property that stops an
  append renaming an item by accident is the same property that leaves a
  corrected item still asserting the refuted claim in the one field anybody
  reads, silently, with exit 0. `mg list` prints titles only, and a `done`
  ticket is precisely the one nobody opens.

- **Four items needed exactly this on 2026-08-13, and one propagated.**
  mg-9cc0 ("evidence ages out within hours" — durable in `launchctl print`,
  verified at 7.5h), mg-8158 ("steps 1, 2 and bridget verification are
  dispatchable" — split to mg-d611), mg-b6bd ("prompts have NO automatic path
  onto disk" — pogod's boot installs them), and a mayor figure of 156 against a
  shipped 150. All four were corrected in the body and retitled afterwards. In
  between, **mg-8074's worker repeated mg-b6bd's refuted premise verbatim** —
  hours after that body refuted it, in its *unverified* list, **citing mg-b6bd
  as the source.** It was being careful, and being careful is what carried the
  claim: a cited premise reads as sourced, and the source was a title.

- **Deliberately a prompt line and not a detector**, per Daniel's ruling on
  pogo#144 the same day that keeping a control small *is* the control. A
  detector would have to judge whether a title "still asserts" what a body
  corrected, which is the expensive half of the problem; the cheap half is the
  agent already editing the body. There is no title/body-agreement check, no
  new gate, and no compliance ticket. The one assertion added is a presence pin
  on the shipped text, in the test that already pins the rest of that block.

- **The remedy names an instrument that was measured, not assumed.** `--title`
  and `--append-body-file` compose in a single `mg edit` invocation — checked
  against a sandbox store (`mg --root=...`): one command retitled the item and
  appended the correction, exit 0, and `mg list` then showed the corrected
  title. `--title` on its own rewrites the `# ` heading line in place and
  touches no other byte of the body, so the retitle cannot race the prose.
