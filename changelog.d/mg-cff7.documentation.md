- **Why the gated-close refusal at merge covers only UNCLAIMED items (mg-cff7).** `CloseMGWorkItemAtMerge` and
  `ErrMGWorkItemGated` now state the rationale that lived only in test comments: `human`, `parked` and
  `blocked:<agent>` are dispatch gates, a claimed item already has a worker, and the merging branch is that worker's,
  so its merge closes the item as an ordinary completion. No behaviour change. Refs drellem2/pogo#198.
