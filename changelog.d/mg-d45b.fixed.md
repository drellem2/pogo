- **The preservation record gains the branch and the modified/untracked split, so
  a preserved worktree can be triaged without opening it (mg-d45b).**
  `worktree_preserved` now carries `branch` (or `branch_error`), `modified_paths`
  and `untracked_paths` alongside the total it already had.

  **MOST OF THIS TICKET WAS ALREADY FIXED BEFORE IT WAS WORKED, and saying so is
  part of the fix.** It was filed at 02:45Z asking for a structured event at the
  preservation site, where there was none — three days of preservations had to be
  reconstructed with `grep -rl 'preserved uncommitted work' ~/.macguffin/mail/mayor`
  plus unstructured `log.Printf` lines, and that worked only because someone knew
  the exact English phrase to grep for. mg-32e3 landed `worktree_preserved` at
  04:54Z, three hours later and independently. The finding was accurate when
  written; the ticket's remaining content is the two fields its requirement named
  that the landed event does not carry.

  **THE BRANCH IS THE FIELD THE RETENTION IS ABOUT.** A preserved worktree keeps
  its branch checked out — that is what pinning *means* — so the branch cannot be
  deleted, cannot be re-used, and is where a rescuer has to start. The mail said it
  and the record did not, so any consumer built on the event had to go back to the
  mailbox for it, which is the split this ticket exists to close. It is read from
  the tree with `git rev-parse` rather than copied from the registry record,
  because a name copied from elsewhere is a claim that can rot while an observation
  cannot.

  **AND `dirty_paths: 16` FUSES TWO FACTS WITH DIFFERENT CONSEQUENCES.** A modified
  tracked path still has its committed version in the object store, so the exposure
  is a lost edit. An untracked path is on no branch, in no stash and on no remote,
  and the preserved tree is its **only copy on the machine** — that is how
  `~/.pogo/polecats/qbe37` came to hold the sole copy of an entire 1450-line
  `internal/strandwatch/` package. One number cannot distinguish sixteen tweaks
  from sixteen irreplaceable files, so a reader deciding whether a preservation was
  urgent had to open the tree and look: the by-hand reconstruction the event was
  built to replace, surviving inside the event itself.

  **THE FIX IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so the three ways it
  could repeat it are closed and each is proven able to fail.** (1) A `branch` key
  that simply disappears when the read fails would make an unreadable branch and an
  unimplemented field the same artifact to every consumer — *the field is missing
  exactly when something went wrong* is how three days of preservations became
  unqueryable in the first place. So a failed read emits `branch_error`, the rule
  `workItemLine` already applies to a missing work item, and the two keys are
  alternatives so `branch` can be trusted when present. (2) Computing the split
  from `Files` — capped at ten for legibility — would silently under-report every
  tree with more than ten changes, a number reconstructed from a partial record;
  the counts are taken over the full porcelain output, and a fixture with 12
  modified and 14 untracked paths asserts both that they are uncapped *and* that
  the capped computation would have given a different answer, so the test could not
  have passed against the shortcut. (3) `emitWorktreePreserved` took five
  positional arguments and the counts would have made seven — in a file whose own
  comment records that a positional call which compiles is a call that looks
  complete. The counts travel as the `*DirtyWorktreeError` they come from instead,
  and `nil` states *the tree was not read* in a way a zero cannot.

  **AN UNREADABLE TREE STILL REPORTS NO COUNTS.** On `outcome: undetermined`
  `git status` failed, so `dirty_paths`, `modified_paths` and `untracked_paths` are
  all absent rather than `0` — a zero there reads as "clean", which is precisely the
  claim mg-4d45 exists to keep this path from making. `branch_error` is the norm on
  that path for the same underlying reason.

  **The mail is untouched**, deliberately: 22 delivered notices over three days are
  the proof it works, and it is the validated template mg-be37 was told to copy.
  `DirtyWorktreeError.Error()` is unchanged too, since the notice body interpolates
  it. Documented in [docs/event-log.md](../docs/event-log.md) and
  [docs/operations.md](../docs/operations.md).
