package enthvcs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// Presubmit is `pogo presubmit` on an enth checkout (docs/design/enth-vcs.md
// §12, slice 6, mg-30da0): would submitting item from the checkout at root
// deliver the work in it? It returns the "pre-submit OK: ..." line, or an
// error that is the refusal's text (the caller prefixes REFUSING TO SUBMIT).
//
// In order: the checkout is clean (Dirty); its base is not a trailer-less
// unlanded Change (the trailer-independent signal, §5 guard 1); the item's
// unlanded Changes (filtered by worker when given) form exactly one stack;
// and that stack's top is the checkout's base. A check that cannot run
// refuses: it never reads as clean. bin is the enth executable ("" = PATH).
func Presubmit(ctx context.Context, root, item, worker, bin string) (string, error) {
	msg, _, _, err := PresubmitStack(ctx, root, item, worker, bin)
	return msg, err
}

// PresubmitStack is Presubmit that also returns the stack it approved and the
// backend that listed it — what the worker then lands (mg-5b904).
func PresubmitStack(ctx context.Context, root, item, worker, bin string) (string, vcs.Submission, Backend, error) {
	if item == "" {
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("%s is an enth checkout, and on enth the work item names the work: pass --item=<work item id>", root)
	}
	change := fmt.Sprintf("enth change %s -m \"…\" --trailer %s=%s --trailer %s=%s",
		root, vcs.TrailerItem, item, vcs.TrailerWorker, workerOr(worker))
	files, err := Backend{Bin: bin}.Dirty(ctx, root)
	if err != nil {
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("enth status failed — cannot tell whether this checkout holds uncommitted work: %v", err)
	}
	if len(files) > 0 {
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("uncommitted work in this checkout — make it a Change (%s), or discard it deliberately:\n%s",
			change, strings.Join(files, "\n"))
	}
	b, base, err := WorkspaceBase(root, bin)
	if err != nil {
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("cannot read this checkout's base: %v", err)
	}
	if err := b.UntrailedBase(ctx, root, base); err != nil {
		return "", vcs.Submission{}, Backend{}, err
	}
	s, err := b.FindStack(ctx, item, worker)
	var amb *vcs.AmbiguousWorkError
	switch {
	case errors.Is(err, vcs.ErrNoWork):
		who := vcs.TrailerItem + "=" + item
		if worker != "" {
			who += " " + vcs.TrailerWorker + "=" + worker
		}
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("no unlanded Change carries %s — nothing would be delivered. Make your Change with: %s", who, change)
	case errors.As(err, &amb):
		return "", vcs.Submission{}, Backend{}, err
	case err != nil:
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("cannot list the Changes of %s on %s: %v", item, b.Repo.Remote, err)
	}
	if s.Ref != base {
		return "", vcs.Submission{}, Backend{}, fmt.Errorf("this checkout's base %s is not the top of %s's stack (%s): what would be submitted is not the work in this checkout",
			base, item, s.Ref)
	}
	return fmt.Sprintf("pre-submit OK: %d Change(s) for %s, top %s, checkout clean, durable on %s",
		len(s.Changes), item, s.Ref, b.Repo.Remote), s, b, nil
}

func workerOr(w string) string {
	if w == "" {
		return "$POGO_AGENT_NAME"
	}
	return w
}

// PrintStackLine is what `pogo presubmit --print-stack` prints on stdout: the
// approved stack's Change ids, bottom first, on ONE line, separated by single
// spaces. Two consumers rely on exactly this (mg-88ece): the polecat
// templates' self_land, which splits it on any whitespace into one argument per
// id, and the CONFLICT/RED re-make step, which puts it whole into a
// Pogo-Supersedes trailer value — which must stay on one line, and which
// vcs.DropSuperseded splits back into ids with strings.Fields. Change it and
// both break.
func PrintStackLine(ids []string) string {
	return strings.Join(ids, " ")
}
