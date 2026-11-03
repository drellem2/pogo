package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/strandedwork"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// Polecat spawn onto an enth repo (docs/design/enth-vcs.md §4, slice 5,
// mg-cc5cd).
//
// A repo whose project record says `"vcs": "enth"` gets an enth checkout at
// the polecat's directory instead of a git worktree:
//
//	enth --remote URL --repo NAME checkout <target ref> DIR [workspace_paths...]
//
// There is no polecat-<name> branch, so none of the git spawn's branch
// machinery (leftover reclaim, the branch rollback, the base fallback chain)
// runs. What stays the same is the directory discipline: a directory this
// spawn did not create is never removed on failure (drellem2/pogo#167).

// enthWorkerEnv is what an enth polecat's environment adds (§4,
// "Environment"): the repo's enthd endpoint and repo name, so every `enth`
// verb the worker runs goes to enthd and every Change is durable there at
// creation (P4), and the token the worker acts as.
//
// The token (mg-3a813): when the project record names a token file, tok is
// what spawnEnthToken read from it and the worker gets exactly that — it is
// appended after os.Environ() and so overrides any $ENTH_TOKEN pogod itself
// holds. Only this project's polecats get it; nothing else pogod spawns does.
// With no file named (tok == ""), the token pogod itself holds in $ENTH_TOKEN
// is passed through, when it holds one; without that enth falls back to the
// token `enth login` saved for the remote, which the worker reads from the
// same HOME. Either way it is passed through and never logged.
//
// ENTH_TRUNK is the ref `enth land` defaults to. The worker does not land, but
// a verb that reads it should read this repo's trunk, not enth's guess.
//
// The worker also gets an identity for `enth change` when enth would find
// none (enthvcs.IdentityEnv, mg-30da0): enth reads no git config, so on a host
// whose only identity is ~/.gitconfig every Change would be refused
// no_identity. The identity is git's for the source repo — what a git
// polecat's commits carry.
func enthWorkerEnv(e vcs.EnthRepo, tok, sourceRepo, worker string) []string {
	env := []string{"ENTH_REMOTE=" + e.Remote, "ENTH_REPO=" + e.Repo}
	if e.Trunk != "" {
		env = append(env, "ENTH_TRUNK="+e.Trunk)
	}
	if tok == "" {
		tok = os.Getenv(vcs.EnthTokenEnv)
	}
	if tok != "" {
		env = append(env, vcs.EnthTokenEnv+"="+tok)
	}
	name, email := gitIdentity(sourceRepo)
	return append(env, enthvcs.IdentityEnv(os.Getenv, name, email, worker)...)
}

// spawnEnthToken is the project's own token for an enth polecat spawn, read
// from the record's token file (mg-3a813). "" with no error means the record
// names no file: the worker gets today's token. A named file that cannot be
// read refuses the spawn, naming the path — it never falls back to pogod's
// token or the shared `enth login`, because that would run the worker as
// someone the project's owner did not choose.
func spawnEnthToken(e vcs.EnthRepo) (string, error) {
	tok, _, err := e.Token()
	if err != nil {
		return "", fmt.Errorf("refusing to spawn onto enth repo %s: the project record names %v. "+
			"Nothing was dispatched, and the shared enth login was NOT used in its place. Fix the file, or "+
			"change the record with `pogo project set-enth-token`", e.Repo, err)
	}
	return tok, nil
}

// gitIdentity is git's user.name / user.email as seen from dir (the repo's
// own config, then the global one). Either is "" when unset.
var gitIdentity = func(dir string) (name, email string) {
	get := func(key string) string {
		c := exec.Command("git", "config", "--get", key)
		c.Dir = dir
		out, err := c.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}

// enthSelectionPrelude is the prompt block for a NARROWED enth workspace
// (§4, "the escape hatch"): the checkout holds only the record's
// workspace_paths, an edit elsewhere is refused by enth as outside_selection
// when the worker makes its Change, and `enth checkout --add` is the remedy.
// A whole-repo workspace (the default) gets nothing: there is no selection to
// fall outside.
func enthSelectionPrelude(e vcs.EnthRepo, dir string) string {
	if len(e.WorkspacePaths) == 0 {
		return ""
	}
	return fmt.Sprintf("> **Your workspace is a NARROWED enth checkout.** It holds only: %s.\n"+
		"> A new file outside those paths is refused when you make your Change (`outside_selection`).\n"+
		"> Before you touch anything else, widen the checkout: `enth checkout --add <PATH> %s`.\n\n",
		strings.Join(quoted(e.WorkspacePaths), ", "), dir)
}

func quoted(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = "`" + p + "`"
	}
	return out
}

// enthAdoption is --stranded-adopt on an enth repo (§4, W7; slice 6,
// mg-30da0): the polecat's checkout is made AT the stranded worker's top
// unlanded Change, found by the item's Pogo-Item trailer, so its stack is the
// tree the worker wakes up in.
type enthAdoption struct {
	Stack  vcs.Submission
	Reason string
}

// enthStacks is the listing an enth adoption resolves through. A var so tests
// can stand in for enthd.
var enthStacks = func(e vcs.EnthRepo, item string) (vcs.Submission, error) {
	return enthvcs.Backend{Repo: e}.FindStack(context.Background(), item, "")
}

// resolveEnthAdoption finds the one stack an adopt dispatch continues, or
// says why there is none. Ambiguity is refused, not resolved: two workers'
// stacks for one item are a decision for whoever dispatches, as on git.
func resolveEnthAdoption(e vcs.EnthRepo, item, reason string) (enthAdoption, string) {
	if item == "" {
		// An empty item would list EVERY unlanded Change in the repo.
		return enthAdoption{}, fmt.Sprintf("--stranded-adopt on %s needs the work item (--id): on enth the item's "+
			"%s trailer is what names the work to adopt. Nothing was dispatched", e.Repo, vcs.TrailerItem)
	}
	s, err := enthStacks(e, item)
	var amb *vcs.AmbiguousWorkError
	switch {
	case errors.Is(err, vcs.ErrNoWork):
		return enthAdoption{}, fmt.Sprintf("--stranded-adopt on %s: %s has no unlanded Change carrying %s=%s, so there "+
			"is nothing to adopt. Nothing was dispatched (dispatch without --stranded-adopt to start from the trunk)",
			e.Repo, item, vcs.TrailerItem, item)
	case errors.As(err, &amb):
		return enthAdoption{}, fmt.Sprintf("--stranded-adopt on %s: %v. Nothing was dispatched", e.Repo, err)
	case err != nil:
		return enthAdoption{}, fmt.Sprintf("--stranded-adopt on %s: listing %s's Changes on %s failed: %v. Nothing was dispatched",
			e.Repo, item, e.Remote, err)
	}
	return enthAdoption{Stack: s, Reason: reason}, ""
}

// record is the adoption as the dispatch_stranded_work_adopted event states
// it, in the git adoption's fields: the "branch" is the stack's top.
func (a enthAdoption) record(trunk string) strandedAdoption {
	return strandedAdoption{
		Finding: strandedwork.Finding{Branch: "enth stack of " + a.Stack.Item, Ref: a.Stack.Ref, Target: trunk, Pushed: true},
		Reason:  a.Reason,
	}
}

// enthAdoptPrelude is the prompt block that tells the worker what it is
// standing on.
func enthAdoptPrelude(item string, a enthAdoption) string {
	worker := a.Stack.Worker
	if worker == "" {
		worker = "(no Pogo-Worker trailer)"
	}
	return fmt.Sprintf("# ⚠ THIS IS AN ADOPTION — your enth checkout is based on %s, NOT on the trunk\n\n"+
		"Work item %s already had %d unlanded Change(s) by %s, and the dispatcher chose to CONTINUE them rather "+
		"than start over. The stated reason was:\n\n    %s\n\n"+
		"Those Changes are the work you were sent to land: do not re-derive them. A Change you make here stacks on "+
		"top of them; make it with the same `--trailer %s=%s` so the stack stays one submission.\n\n",
		a.Stack.Ref, item, len(a.Stack.Changes), worker, a.Reason, vcs.TrailerItem, item)
}

// createEnthPolecatWorkspace makes the enth checkout at dir for a polecat on
// sourceRepo aimed at target. preexisted says dir was there before this
// spawn: enth refuses an existing directory anyway, and this refuses first so
// the failure path has nothing to decide about removing it. A failed
// checkout is rolled back only when the directory is the spawn's own.
//
// adoptAt, when set, is the Change an adoption checks out instead of target's
// tip. The checkout is then verified to be AT it, read back from
// .enth/checkout — the enth form of git's `merge-base --is-ancestor` check —
// and refused, rolled back, if it is not.
func createEnthPolecatWorkspace(e vcs.EnthRepo, sourceRepo, dir, target, adoptAt string, preexisted bool) (vcs.Workspace, string, error) {
	b := enthvcs.Backend{Repo: e}
	ctx := context.Background()
	ref := b.ResolveBase(ctx, vcs.Repo{Path: sourceRepo}, target)
	if adoptAt != "" {
		ref = adoptAt
	}
	if preexisted {
		return vcs.Workspace{}, ref, fmt.Errorf("%s already exists, and an enth checkout is made only into a new "+
			"directory. It was not created by this spawn and was not touched; establish who owns it "+
			"(`pogo agent list`; `enth status %s`) before reclaiming it (drellem2/pogo#167)", dir, dir)
	}
	ws, err := b.Create(ctx, vcs.WorkspaceSpec{Repo: vcs.Repo{Path: sourceRepo}, Dir: dir, Base: ref})
	if err != nil {
		cause, out := commandErrorParts(err)
		_ = b.Rollback(ctx, vcs.Repo{Path: sourceRepo}, dir)
		return vcs.Workspace{}, ref, fmt.Errorf("enth checkout of %s (%s on %s) failed: %v\n%s", ref, e.Repo, e.Remote, cause, out)
	}
	if adoptAt != "" {
		st, err := enthvcs.ReadState(dir)
		if err != nil || st.Base != adoptAt {
			_ = b.Rollback(ctx, vcs.Repo{Path: sourceRepo}, dir)
			return vcs.Workspace{}, ref, fmt.Errorf("adopting %s: the checkout's base reads back as %q (%v), not the adopted Change; "+
				"rolled back, nothing was dispatched", adoptAt, st.Base, err)
		}
	}
	return ws, ref, nil
}

// enthTemplateRefusal is the spawn refusal of §12 (slice 11, mg-7c936): an
// enth workspace needs a template that branches on .VCS. The body checked is
// the one the worker would be rendered from — the resolved template plus its
// drop-ins — because that is what reaches the worker, and an installed copy
// under ~/.pogo/agents/templates can predate the enth blocks. Such a template
// renders its git protocol (git commit, git push, pogo refinery submit) for a
// directory with no .git, so the spawn is refused before any side effect
// rather than dispatched into a worker that cannot follow its own prompt.
//
// "" means the template branches and the spawn proceeds.
func enthTemplateRefusal(tmplPath string, e vcs.EnthRepo) string {
	body, err := renderableTemplateBody(tmplPath)
	if err == nil {
		var ok bool
		ok, err = TemplateBranchesOnVCS(body)
		if ok {
			return ""
		}
		if err == nil {
			return fmt.Sprintf("refusing to spawn onto enth repo %s: the template %s (with its drop-ins) never "+
				"references .VCS, so it would hand the worker its git protocol — git commit, git push, "+
				"pogo refinery submit — in an enth checkout with no .git (docs/design/enth-vcs.md §12). It is most "+
				"likely an installed copy from before the enth blocks: reinstall the prompts (pogo agent prompt "+
				"install), or add the enth blocks to a locally edited copy. Nothing was dispatched",
				e.Repo, tmplPath)
		}
	}
	return fmt.Sprintf("refusing to spawn onto enth repo %s: cannot tell whether the template %s branches on "+
		".VCS: %v. Nothing was dispatched", e.Repo, tmplPath, err)
}

// renderableTemplateBody is the text ExpandTemplate parses: the template's
// body with frontmatter stripped, plus its drop-ins.
func renderableTemplateBody(tmplPath string) (string, error) {
	_, body, err := ParsePromptFrontmatter(tmplPath)
	if err != nil {
		return "", err
	}
	drop, err := LoadDropIns(strings.TrimSuffix(filepath.Base(tmplPath), ".md"))
	if err != nil {
		return "", err
	}
	return appendDropIns(body, drop), nil
}
