- **The remaining three `mg new` callers name the repo they mean, so the class
  mg-8595 unblocked cannot re-block the queue from a different file (mg-1eb6).**
  mg-0b57 made `mg new` refuse to record a repo path inside `~/.pogo/polecats` or
  `~/.pogo/refinery/worktrees` — pogo deletes those when the owning agent is
  reaped, so an item filed against one outlives its own repo and fails only when
  somebody finally dispatches it. `7a95348` (mg-8595) fixed the two callers that
  were failing the gate. The audit this ticket asked for found **five callers in
  the whole tree**, so **three were left**:

  - `internal/agent`'s `mgNewClaimed` and `mgNewAvailable` — `--no-repo`. These
    passed, and the reason they passed is not the reason they were right: that
    package's `TestMain` pins `HOME` under a throwaway root, so mg's
    `~`-relative guard did not recognise the real polecat worktree it was
    standing in and recorded the doomed cwd in **silence** instead of refusing
    it. Same call shape, same wrong path, alarm disconnected.
  - `scripts/test-e2e.sh` — `--repo="$WORK_REPO"`, because this item genuinely is
    about a repo: `$WORK_REPO` is the checkout the spawn two lines later hands
    the polecat. It filed by `cd`-ing there and letting mg resolve the repo from
    the cwd, and survived only because `$SANDBOX` is a `mktemp` path rather than
    one of the trees mg refuses.

  **The guard is untouched, and was re-checked after the change** from inside a
  polecat worktree: an omitted `--repo` still exits 2 with the refusal naming
  `~/.pogo/polecats`. `--allow-ephemeral-repo` would also have made every one of
  these pass and is used nowhere.

- **What the audit found nothing of, stated so the next reader does not re-run it
  (mg-1eb6).** No production code path files a work item: every non-test `mg`
  invocation in the tree is `mail send`, `list`, `show`, `done`, `archive`,
  `reopen` or `init`. Verification was `go test -count=1`, not `./build.sh`
  alone — cwd is not part of go's test-cache key while these tests depend on cwd
  at runtime, so a cached `ok` reports green for a test that cannot pass here,
  which is exactly how this survived local runs for as long as it did.

  Left alone as a different question, not silence: the `mg new` examples in
  `install.sh`, `pogo`'s help text and the shipped prompt corpora omit `--repo`,
  but crew agents run from `~/.pogo/agents/<name>`, which is not one of the
  refused trees. Teaching the flag in the corpus is a corpus decision of the
  kind `bodyratchet_test.go` exists to enforce, and belongs to whoever owns that
  ratchet.
