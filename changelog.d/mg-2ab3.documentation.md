- **`gitgc.OwnerGone` is labelled dormant at every site that names it, instead
  of load-bearing at some of them (mg-2ab3).** gh #97 withdrew the ownership arm
  of the cannot-tell rule, so `RemoveWorktree` ignores its `WorktreeOwner` on
  every path and nothing in production constructs `OwnerGone` — both gitgc sweep
  call sites and `cmd/pogod`'s exit hook pass `OwnerUnproven`. Two comments still
  described the withdrawn distinction in the present tense, and one of them sat
  at the exact call site a future caller would read first: *"OwnerGone belongs
  where liveness has been positively excluded … the gitgc sweep, which gates on
  LivePolecats and a concluded ticket before it removes anything."* The sweep
  does not do that.

  Keeping a dormant mechanism is defensible; keeping it under a description the
  code does not honour is not — a dormant mechanism labelled dormant is honest
  inventory, while the same mechanism labelled load-bearing is a trap with a
  maintainer's signature on it, because the next reader defers to the label
  instead of reading the calls. The decision here is **keep and label**, not
  retire: the parameter is mg-4d45's public surface, and deleting it is that
  ticket's call rather than a side effect of this one. Every one of the 16
  `OwnerGone` references was accounted for, and the tests that still name it now
  say what they are — controls that go red if an ownership arm returns, not
  callers. `TestWorktreeDirtyUnclassifiableProceeds` is renamed
  `TestWorktreeDirtyUnclassifiableIsRefused`, since it asserts a refusal.
