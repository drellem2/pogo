package refinery

import (
	"fmt"
	"log"
	"strings"
)

// mergeResult is everything processMerge learned about a merge it attempted.
// It replaced a four-value return once the post-merge step needed to report
// both a SHA and its own error alongside the existing gate/deploy channels.
//
// All fields are meaningful only on a successful merge except GateOutput,
// which is captured on every path including failure and cancellation.
type mergeResult struct {
	// GateOutput is the captured quality-gate output.
	GateOutput string
	// DeployError is non-empty when the per-repo deploy hook ran and failed.
	DeployError string
	// PostMergeError is non-empty when a declared post-merge step ran and
	// failed. Unlike DeployError this is consulted by pogod's reap, which
	// refuses to complete the work item while it is set.
	PostMergeError string
	// MergedSHA is the commit the branch landed as on the target ref.
	MergedSHA string
	// AlreadyMerged marks the no-op path: the branch was an ancestor of the
	// target before processing began.
	AlreadyMerged bool
}

// runPostMergeSteps performs the post-merge protocol the submitter declared on
// this MR, against the SHA the merge actually landed as. It returns an error
// string for MergeRequest.PostMergeError — empty when nothing was declared or
// every step succeeded.
//
// # Why the refinery owns this
//
// Some deliverables follow their own merge rather than being it: a release cut
// merges a version bump and then has to tag the commit that bump landed as.
// Whoever performs that step must satisfy two conditions at once — see
// PostMergeTag for why the authoring polecat and a pre-merge script each
// satisfy exactly one, and why the naive fix for either failure is the other
// failure. The refinery satisfies both, so the step lives here.
//
// The general rule this encodes: an actor that sees the merged SHA and
// outlives the worker performs the post-merge step. Deferring the worker's
// reap — which is what --defer-done, PRFlow (mg-7746) and the post-merge-work
// tag (mg-d86e) all do — keeps the worker as the acting party and only buys it
// more time. Those remain correct for work only the worker can do (opening a
// PR from its own branch, mailing its own report). They are the wrong
// instrument for work that merely needs the merged SHA, and a release cut is
// the case where that distinction stopped being academic.
//
// # Failure policy
//
// Every step here runs after the branch has landed on origin, so nothing is
// unwound and the merge stays successful. What a failure must NOT do is read
// as completion: the defect this closes had a work item marked done with
// exit_code=0 while its tag did not exist, which no backstop could catch
// because the item was in a terminal state. The returned string therefore
// reaches pogod's reap, not just the logs — see resolvePostMergeWork.
func (r *Refinery) runPostMergeSteps(wtDir string, mr *MergeRequest, sha string) string {
	if mr.PostMergeTag == "" {
		return ""
	}

	// A declared step with no SHA to act on is a hard failure, not a skip.
	// rev-parse failing after a successful push is unlikely, but tagging
	// "whatever the target tip is now" instead would reintroduce exactly the
	// dangling-tag defect this design exists to avoid: by the time we looked,
	// another MR may have advanced the target.
	if strings.TrimSpace(sha) == "" {
		err := fmt.Errorf("cannot create tag %q: the merged SHA is unknown, and tagging the current %s tip instead could tag another MR's commit",
			mr.PostMergeTag, mr.TargetRef)
		log.Printf("refinery: MR %s step=post-merge-tag FAILED: %v", mr.ID, err)
		emitPostMergeTagFailed(mr, mr.PostMergeTag, "", err)
		return err.Error()
	}

	if err := r.tagMergedSHA(wtDir, mr, strings.TrimSpace(sha)); err != nil {
		log.Printf("refinery: MR %s step=post-merge-tag FAILED tag=%s sha=%s: %v — the merge has landed; the tag has NOT. The work item will not be completed (mg-6879)",
			mr.ID, mr.PostMergeTag, sha, err)
		emitPostMergeTagFailed(mr, mr.PostMergeTag, sha, err)
		return err.Error()
	}

	log.Printf("refinery: MR %s step=post-merge-tag tag=%s sha=%s pushed to origin (mg-6879)", mr.ID, mr.PostMergeTag, sha)
	emitPostMergeTagged(mr, mr.PostMergeTag, sha)
	return ""
}

// tagMergedSHA creates the declared tag on sha and pushes it to origin.
//
// It is idempotent in the only direction that is safe to be: a tag that
// already exists on origin pointing at sha is treated as success, so a
// resubmitted MR (gh #34) or a retried merge converges instead of failing on
// its own prior work. A tag that exists pointing SOMEWHERE ELSE is a hard
// error and is never moved — a published release tag that silently relocates
// is worse than a missing one, and the disagreement means a human needs to
// look. The local clone's copy of the tag is fair game either way; it is
// private to the refinery and gets realigned from origin.
func (r *Refinery) tagMergedSHA(wtDir string, mr *MergeRequest, sha string) error {
	tag := mr.PostMergeTag
	ref := "refs/tags/" + tag

	// Ask origin, not the local clone: the clone can hold a stale tag from an
	// earlier attempt, and origin is what "the tag exists" has to mean.
	existing, err := remoteTagSHA(wtDir, tag)
	if err != nil {
		return fmt.Errorf("look up tag %s on origin: %w", tag, err)
	}
	if existing != "" {
		if existing == sha {
			log.Printf("refinery: MR %s step=post-merge-tag tag=%s already on origin at %s — nothing to do", mr.ID, tag, sha)
			return nil
		}
		return fmt.Errorf("tag %s already exists on origin at %s but this merge landed as %s — refusing to move a published tag; "+
			"resolve by hand (inspect both commits, then delete and re-push the tag if the existing one is wrong)",
			tag, existing, sha)
	}

	// Drop any local copy so the create below cannot fail on debris from a
	// previous attempt. Errors are ignored: the common case is no such tag.
	gitCmdOutput(wtDir, "tag", "-d", tag)

	// Annotated, not lightweight: `git describe` prefers annotated tags, which
	// is how a release tag is normally read back, and it records who made it
	// and when.
	msg := fmt.Sprintf("%s\n\nTagged by the pogo refinery on merge of %s (MR %s) into %s.", tag, mr.Branch, mr.ID, mr.TargetRef)
	if out, gerr := gitCmdOutput(wtDir, "tag", "-a", tag, "-m", msg, sha); gerr != nil {
		return fmt.Errorf("create tag %s at %s: %s: %w", tag, sha, out, gerr)
	}

	if out, gerr := gitCmdOutput(wtDir, "push", "origin", ref); gerr != nil {
		// Someone may have pushed the same tag between the lookup above and
		// this push. Re-read origin: if it now agrees with us the race was
		// benign, and reporting a failure would block a correct release.
		if raced, rerr := remoteTagSHA(wtDir, tag); rerr == nil && raced == sha {
			log.Printf("refinery: MR %s step=post-merge-tag push of %s was rejected but origin already has it at %s — treating as success", mr.ID, tag, sha)
			return nil
		}
		return fmt.Errorf("push tag %s to origin: %s: %w", tag, out, gerr)
	}
	return nil
}

// remoteTagSHA returns the commit origin's copy of tag points at, or "" when
// origin has no such tag. An annotated tag is resolved through to its commit
// via the ^{} peeled ref, so the answer is comparable with a merge SHA.
//
// ls-remote is used rather than a fetch-then-rev-parse so the check cannot be
// answered from a stale local ref, and so it stays read-only.
func remoteTagSHA(wtDir, tag string) (string, error) {
	ref := "refs/tags/" + tag
	out, err := gitCmdOutput(wtDir, "ls-remote", "origin", ref, ref+"^{}")
	if err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	var direct, peeled string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case ref:
			direct = fields[0]
		case ref + "^{}":
			peeled = fields[0]
		}
	}
	// The peeled value is the commit an annotated tag points at; prefer it.
	// For a lightweight tag there is no peeled line and the direct value
	// already is the commit.
	if peeled != "" {
		return peeled, nil
	}
	return direct, nil
}

// validTagName reports whether name is usable as a git tag, with a reason when
// it is not. It is a deliberately local check on a subset of
// git-check-ref-format's rules, run at submit time so a typo is rejected while
// the submitter is still there to see it — rather than after a merge has
// landed, when the failure costs a half-finished release instead of a retry.
//
// It is conservative: everything it rejects is genuinely invalid, and git
// remains the final authority at tag-creation time.
func validTagName(name string) (bool, string) {
	switch {
	case name == "":
		return false, "empty"
	case strings.HasPrefix(name, "-"):
		return false, "starts with '-', which git parses as a flag"
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return false, "starts or ends with '/'"
	case strings.HasSuffix(name, "."):
		return false, "ends with '.'"
	case strings.HasSuffix(name, ".lock"):
		return false, "ends with '.lock', which git reserves"
	case strings.Contains(name, ".."):
		return false, "contains '..'"
	case strings.Contains(name, "//"):
		return false, "contains '//'"
	case strings.Contains(name, "@{"):
		return false, "contains '@{'"
	}
	for _, r := range name {
		switch {
		case r <= 0x20 || r == 0x7f:
			return false, "contains a space or control character"
		case strings.ContainsRune("~^:?*[\\", r):
			return false, fmt.Sprintf("contains %q, which git forbids in ref names", r)
		}
	}
	return true, ""
}
