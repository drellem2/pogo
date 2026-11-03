# The polecat pre-submit check as the templates carried it from mg-c184d until
# mg-71014 replaced it with `pogo presubmit --item=<id> --target=<target>`.
# Kept verbatim (markdown indentation removed) as the reference the verb is
# measured against: TestPresubmitGitEqualsTheTemplateShell runs both on the
# same trees. Lifted from main:internal/agent/prompts/templates/polecat.md.
presubmit_check() {
  local target="$1" dirty ahead
  git fetch origin || { echo "REFUSING TO SUBMIT: git fetch origin failed — cannot tell what this branch carries"; return 1; }
  dirty=$(pogo worktree-dirty) || { echo "REFUSING TO SUBMIT: pogo worktree-dirty failed — cannot tell whether this worktree holds uncommitted work"; return 1; }
  if [ -n "$dirty" ]; then
    echo "REFUSING TO SUBMIT: uncommitted work in this worktree — commit and push it, or discard it deliberately:"
    echo "$dirty"; return 1
  fi
  ahead=$(git rev-list --count "origin/$target..HEAD") || { echo "REFUSING TO SUBMIT: git rev-list origin/$target..HEAD failed"; return 1; }
  if [ "$ahead" -eq 0 ]; then
    echo "REFUSING TO SUBMIT: $BRANCH carries no commits ahead of origin/$target — nothing would be delivered."
    echo "Commit and push your work. (If an earlier MR of yours already merged this branch, do not resubmit: check it with pogo refinery show <id>.)"
    return 1
  fi
  [ "$(git rev-parse HEAD)" = "$(git rev-parse -q --verify "origin/$BRANCH")" ] || { echo "REFUSING TO SUBMIT: origin/$BRANCH is not your HEAD — git push origin \"$BRANCH\" first"; return 1; }
  echo "pre-submit OK: $ahead commit(s) ahead of origin/$target, worktree clean, branch pushed"
}
