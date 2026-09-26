- **The per-repo cap now charges a gh-issue flow the TWO slots it occupies —
  three builds into one cap-3 repo deadlocked on 2026-09-08 (mg-bf42).**

  On the gh-issue track the builder stays alive through review (the `reviews:`
  exemption, mg-aaf6 / drellem2/pogo#131), so a flow holds a builder slot and a
  reviewer slot for its whole review loop. The cap counted only live workers,
  admitted three builds, and left two open PRs whose reviewers could never
  start while no builder could finish.

  Every live gh-issue builder (`stage: build`/`review`) whose reviewer is not
  running now holds one slot back for it — the refinery reserve's idea
  (mg-3977) applied to a second predictable consumer. A new gh-issue build is
  admitted only if both its slots fit; the reviewer a slot is held for is
  admitted into it. `pogo host load --repo` serves `review_slot_holds` and
  `would_refuse_gh_issue_build`. See `docs/CONFIGURATION.md`.
