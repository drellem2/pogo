package enthvcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// Refs on enth (enth-vcs §8, slice 12, mg-ae4ac): R3, R4, R18, L4.
//
//   - Default is the project record's trunk. enth resolves refs on enthd, so
//     there is no origin/HEAD to ask.
//   - Tag is `enth tag NAME C` (enth #9 K, mg-40d5e), a CAS create of
//     refs/tags/NAME. An enth tag is immutable: it can never be deleted and
//     re-made the way git's local tag is. So the idempotent retry READS the
//     refusal instead. A `tag_exists` whose current value is the Change asked
//     for is success (vcs.TagAlreadyPresent); any other value is an error, and
//     the tag is never moved.
//   - CreateRef is `enth ref create refs/heads/NAME C`, with the same reading
//     of `ref_exists`: a branch already at C is success, and a branch
//     elsewhere is an error, never re-carved.
//
// On a followed (import-written) repo every ref move is refused
// `source_authoritative` (enth #9 §4). That is reported as vcs.ErrUnsupported
// and wraps enth's refusal, so a caller can test either.

// RefMove is what a ref verb did: the full ref, the Change it names now, and
// whether it already named that Change before the call (the idempotent retry).
type RefMove struct {
	Ref    string
	Change string
	// Entry is the ref record's log entry; 0 when the ref already existed and
	// nothing was written.
	Entry int64
	// Existed: the ref already named Change, and this call wrote nothing.
	Existed bool
}

// refLine is enth-ref's one stdout line:
// {"type":T,"ref":R,"entry":N,"change":C,"user":U}.
type refLine struct {
	Type   string `json:"type"`
	Ref    string `json:"ref"`
	Entry  int64  `json:"entry"`
	Change string `json:"change"`
}

// Default is the trunk's branch name: refs/heads/main is "main".
func (b Backend) Default(context.Context, vcs.Repo) (string, error) {
	return strings.TrimPrefix(b.trunk(), "refs/heads/"), nil
}

// CreateRef creates the branch ref at at. ref is a bare branch name or a
// full refs/heads/ ref; at is a Change id (or a unique hex prefix), a branch
// name, or a full ref, and "" is the trunk. See CreateBranch.
func (b Backend) CreateRef(ctx context.Context, _ vcs.Repo, ref, at string) error {
	_, err := b.CreateBranch(ctx, ref, at)
	return err
}

// Tag creates the tag name at Change at. msg is not used: an enth tag has no
// message of its own, and enth's ref record keeps who made it and when. See
// TagChange.
func (b Backend) Tag(ctx context.Context, _ vcs.Repo, name, at, _ string) (vcs.TagResult, error) {
	m, err := b.TagChange(ctx, name, at)
	if err != nil {
		return 0, err
	}
	if m.Existed {
		return vcs.TagAlreadyPresent, nil
	}
	return vcs.TagCreated, nil
}

// TagChange is `enth tag name C`, with C resolved from at first so that the
// comparison with a `tag_exists`'s current value is between two full ids.
func (b Backend) TagChange(ctx context.Context, name, at string) (RefMove, error) {
	if strings.TrimSpace(name) == "" {
		return RefMove{}, fmt.Errorf("enth tag: no tag name")
	}
	if strings.TrimSpace(at) == "" {
		return RefMove{}, fmt.Errorf("enth tag %s: no Change to tag — refusing to tag whatever a ref holds now", name)
	}
	id, err := b.ResolveChange(ctx, at)
	if err != nil {
		return RefMove{}, fmt.Errorf("enth tag %s: resolve %s: %w", name, at, err)
	}
	ref := name
	if !strings.HasPrefix(ref, "refs/tags/") {
		ref = "refs/tags/" + name
	}
	out, err := b.run(ctx, append(b.remote(), "tag", name, id)...)
	if err == nil {
		return parseRefLine(out, "tag")
	}
	var re *vcs.RefusedError
	if errors.As(err, &re) {
		switch re.Code {
		case "tag_exists":
			if re.Current == id {
				return RefMove{Ref: ref, Change: id, Existed: true}, nil
			}
			return RefMove{}, fmt.Errorf("tag %s already exists on %s at %s, but this land is %s — refusing to move a published tag "+
				"(an enth tag is immutable; resolve by hand: inspect both Changes, and only the repo's owner can `enth tag --force-delete %s %s`): %w",
				ref, b.where(), re.Current, id, name, re.Current, err)
		case "source_authoritative":
			return RefMove{}, b.followed("tag "+ref, err)
		}
	}
	return RefMove{}, fmt.Errorf("enth tag %s %s on %s: %w", name, id, b.where(), err)
}

// CreateBranch is `enth ref create refs/heads/<ref> C`, with C resolved from
// at ("" is the trunk's tip: --auto-create-target's "branch off the default
// branch").
func (b Backend) CreateBranch(ctx context.Context, ref, at string) (RefMove, error) {
	if strings.TrimSpace(ref) == "" {
		return RefMove{}, fmt.Errorf("enth ref create: no ref")
	}
	full := b.ResolveBase(ctx, vcs.Repo{}, ref)
	if !strings.HasPrefix(full, "refs/heads/") {
		return RefMove{}, fmt.Errorf("enth ref create %s: only a branch (refs/heads/...) is created this way; a tag is a tag", full)
	}
	if full == b.trunk() {
		return RefMove{}, fmt.Errorf("enth ref create %s: that is this repo's trunk", full)
	}
	id, err := b.ResolveChange(ctx, at)
	if err != nil {
		if at == "" {
			at = b.trunk()
		}
		return RefMove{}, fmt.Errorf("enth ref create %s: resolve %s: %w", full, at, err)
	}
	out, err := b.run(ctx, append(b.remote(), "ref", "create", full, id)...)
	if err == nil {
		return parseRefLine(out, "ref_created")
	}
	var re *vcs.RefusedError
	if errors.As(err, &re) {
		switch re.Code {
		case "ref_exists":
			if re.Current == id {
				return RefMove{Ref: full, Change: id, Existed: true}, nil
			}
			return RefMove{}, fmt.Errorf("branch %s already exists on %s at %s, not %s — refusing to re-carve it (a release branch is cut once): %w",
				full, b.where(), re.Current, id, err)
		case "source_authoritative":
			return RefMove{}, b.followed("ref create "+full, err)
		}
	}
	return RefMove{}, fmt.Errorf("enth ref create %s %s on %s: %w", full, id, b.where(), err)
}

// ResolveChange is the full Change id at names: "" is the trunk's tip, a hex
// string is a Change id or a unique prefix of one (a branch of that name is
// tried when no Change matches), and anything else is a branch name or a
// full ref, read at its tip.
func (b Backend) ResolveChange(ctx context.Context, at string) (string, error) {
	at = strings.TrimSpace(at)
	if at == "" {
		return b.RefTip(ctx, "")
	}
	if isHex(at) && len(at) >= 4 {
		l, err := b.show(ctx, at)
		if err == nil {
			return l.Change, nil
		}
		var re *vcs.RefusedError
		if !errors.As(err, &re) || re.Code != "not_found" {
			return "", err
		}
		if tip, terr := b.RefTip(ctx, at); terr == nil {
			return tip, nil
		}
		return "", err
	}
	return b.RefTip(ctx, at)
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return s != ""
}

func parseRefLine(out []byte, want string) (RefMove, error) {
	var l refLine
	if err := json.Unmarshal(bytes.TrimSpace(out), &l); err != nil || l.Type != want || l.Ref == "" || l.Change == "" {
		return RefMove{}, fmt.Errorf("enth %s: unreadable answer %q", want, bytes.TrimSpace(out))
	}
	return RefMove{Ref: l.Ref, Change: l.Change, Entry: l.Entry}, nil
}

func (b Backend) where() string {
	if b.Repo.Remote == "" {
		return b.Repo.Repo
	}
	return b.Repo.Repo + " (" + b.Repo.Remote + ")"
}

// followed is the refusal for a ref move on a repo enth follows from git.
func (b Backend) followed(op string, err error) error {
	return fmt.Errorf("enth %s: %s is followed from its git source, and enth refuses every ref move there until the handover (enth #9 §4, #11): %w: %w",
		op, b.where(), vcs.ErrUnsupported, err)
}
