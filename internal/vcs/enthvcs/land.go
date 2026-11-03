package enthvcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// The enth Lander (docs/design/enth-vcs.md §6.1, slice 7, mg-1fe2b).
//
// A merge queue on enth is three verbs, all through the repo's enthd:
//
//	enth land C1…Cn --dry-run            → T (old) and N (new); stores the
//	                                        rebased Changes (E1, shipped in #9 D)
//	enth checkout N DIR                   → the tree that is gated
//	enth land C1…Cn --expect-tip T --expect-new N --request-id R
//	                                      → lands exactly N, or refuses
//	                                        (stale_tip, result_mismatch)
//
// so the tree that was gated is the tree that lands, or nothing lands. The
// request id makes the land replayable: the same id and Changes again answer
// what the first land answered and land nothing (measured 2026-11-03 on
// enth 0.2.0-dev, including after the tip had moved on to N).
//
// MEASURED HAZARD, the reason the refinery persists a land before issuing it:
// a land of already-landed Changes under a NEW request id is not refused — it
// lands them a second time as a new trunk entry. So a land whose outcome is
// unknown (the connection dropped) must be re-issued with its ORIGINAL
// request id before anything else is tried, never re-planned under a new one.

// landLine is `enth land`'s stdout line: dry_run, landed or conflict.
type landLine struct {
	Type    string             `json:"type"`
	Ref     string             `json:"ref"`
	Entry   int                `json:"entry"`
	Old     *string            `json:"old"`
	New     string             `json:"new"`
	Changes []vcs.LandedChange `json:"changes"`
	// conflict lines
	Change string          `json:"change"`
	Path   json.RawMessage `json:"path"`
	Kind   string          `json:"kind"`
}

// Conflict is one conflicting path a land reported.
type Conflict struct {
	Change string
	Path   string
	Kind   string
}

// LandConflictError is a land (dry or real) refused with code conflict: the
// work does not apply on the tip. Lines are enth's conflict lines verbatim.
type LandConflictError struct {
	Conflicts []Conflict
	Lines     []string
	Err       error
}

func (e *LandConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "enth land: %d conflicting path(s), nothing landed:", len(e.Conflicts))
	for _, c := range e.Conflicts {
		fmt.Fprintf(&b, "\n  %s (%s) in Change %s", c.Path, c.Kind, short(c.Change))
	}
	return b.String()
}

func (e *LandConflictError) Unwrap() error { return e.Err }

// RefusalCode is enth's stable error code carried by err, or "" when err is
// not an enth refusal (a failed exec, an unreadable answer).
func RefusalCode(err error) string {
	var re *vcs.RefusedError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// refusedCodes are the enth land refusals that establish NOTHING LANDED:
// enthd evaluated the request and turned it down (exit 3, `enth land --help`),
// or never accepted it (unauthorized, forbidden). unreachable is deliberately
// not here — a connection that dropped mid-request may have landed — nor is
// any code this list does not know.
var refusedCodes = map[string]bool{
	"conflict": true, "stale_tip": true, "result_mismatch": true, "unlanded_parent": true,
	"base_not_on_ref": true, "base_too_old": true, "rebase_merge": true, "already_landed": true,
	"not_found": true, "ambiguous": true, "bad_signature": true, "forbidden_path": true,
	"request_id_reused": true, "not_a_store": true, "unauthorized": true, "forbidden": true,
}

// LandedNothing reports whether a failed land is known to have landed
// nothing. false means the outcome is unknown, and the land must be replayed
// under the same request id before anything is re-planned.
func LandedNothing(err error) bool { return refusedCodes[RefusalCode(err)] }

func (b Backend) landRef(target string) string {
	return b.ResolveBase(context.Background(), vcs.Repo{}, target)
}

// parseLand reads `enth land`'s stdout. want is the line type expected on
// success ("dry_run" or "landed").
func parseLand(out []byte, want string) (vcs.LandRecord, []Conflict, []string, error) {
	var rec vcs.LandRecord
	var conflicts []Conflict
	var lines []string
	found := false
	for _, raw := range strings.Split(string(out), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var l landLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			return rec, nil, nil, fmt.Errorf("enth land: unreadable line %q: %v", raw, err)
		}
		switch l.Type {
		case "conflict":
			p, err := decodeB(l.Path)
			if err != nil {
				p = string(l.Path)
			}
			conflicts = append(conflicts, Conflict{Change: l.Change, Path: p, Kind: l.Kind})
			lines = append(lines, raw)
		case want:
			found = true
			rec = vcs.LandRecord{Ref: l.Ref, Entry: l.Entry, New: l.New, Changes: l.Changes}
			if l.Old != nil {
				rec.Old = *l.Old
			}
		}
	}
	if !found && len(conflicts) == 0 {
		return rec, nil, nil, fmt.Errorf("enth land: no %s line in %q", want, bytes.TrimSpace(out))
	}
	return rec, conflicts, lines, nil
}

// land runs `enth land` through enthd and parses its answer.
func (b Backend) land(ctx context.Context, want string, args []string, ids []string) (vcs.LandRecord, error) {
	full := append(b.remote(), "land")
	full = append(full, args...)
	full = append(full, ids...)
	out, err := run(ctx, b.Bin, full...)
	rec, conflicts, lines, perr := parseLand(out, want)
	if err != nil {
		if RefusalCode(err) == "conflict" {
			return vcs.LandRecord{}, &LandConflictError{Conflicts: conflicts, Lines: lines, Err: err}
		}
		return vcs.LandRecord{}, err
	}
	if perr != nil {
		return vcs.LandRecord{}, perr
	}
	return rec, nil
}

// DryRun plans landing ids on target: T is rec.Old, N is rec.New, and the
// rebased Changes are stored so N can be checked out (E1).
func (b Backend) DryRun(ctx context.Context, target string, ids []string) (vcs.LandRecord, error) {
	return b.land(ctx, "dry_run", []string{"--dry-run", "--ref", b.landRef(target)}, ids)
}

// LandExpect lands ids on target only if target is at tip and the result is
// exactly n, under requestID. tip "" expects the ref not to exist.
func (b Backend) LandExpect(ctx context.Context, target string, ids []string, tip, n, requestID string) (vcs.LandRecord, error) {
	if tip == "" {
		tip = "-"
	}
	args := []string{"--ref", b.landRef(target), "--expect-tip", tip, "--expect-new", n}
	if requestID != "" {
		args = append(args, "--request-id", requestID)
	}
	rec, err := b.land(ctx, "landed", args, ids)
	if err == nil {
		rec.RequestID = requestID
	}
	return rec, err
}

// Prepare is §6.1's rebase step: a dry run of mr.Changes on mr.Target, then a
// whole-repo checkout of its result N into mr.Dir, which must not exist. The
// gate runs in that checkout. A conflict is a *LandConflictError.
func (b Backend) Prepare(ctx context.Context, mr vcs.MR) (vcs.Prepared, error) {
	if len(mr.Changes) == 0 {
		return vcs.Prepared{}, fmt.Errorf("enth %s: MR %s has no Changes to land", b.Repo.Repo, mr.ID)
	}
	rec, err := b.DryRun(ctx, mr.Target, mr.Changes)
	if err != nil {
		return vcs.Prepared{}, err
	}
	if rec.New == "" {
		return vcs.Prepared{}, fmt.Errorf("enth %s: the dry run of MR %s named no result", b.Repo.Repo, mr.ID)
	}
	if mr.Dir != "" {
		if err := b.CheckoutWhole(ctx, rec.New, mr.Dir); err != nil {
			return vcs.Prepared{}, fmt.Errorf("enth %s: check out the dry run's result %s for the gate: %w", b.Repo.Repo, short(rec.New), err)
		}
		st, err := ReadState(mr.Dir)
		if err != nil {
			return vcs.Prepared{}, fmt.Errorf("enth %s: the gate checkout %s cannot be read: %w", b.Repo.Repo, mr.Dir, err)
		}
		if st.Base != rec.New {
			return vcs.Prepared{}, fmt.Errorf("enth %s: the gate checkout is at %s, not the dry run's result %s", b.Repo.Repo, st.Base, rec.New)
		}
	}
	return vcs.Prepared{MR: mr, GateDir: mr.Dir, Tip: rec.Old, New: rec.New, Plan: rec.Changes}, nil
}

// CheckoutWhole checks rev out of the repo's enthd into dir, which must not
// exist. Never narrowed by workspace_paths: a gate tests the whole tree that
// lands.
func (b Backend) CheckoutWhole(ctx context.Context, rev, dir string) error {
	_, err := run(ctx, b.Bin, append(b.remote(), "checkout", rev, dir)...)
	return err
}

// Land is §6.1's land step: exactly p.New on p.Tip, under p.MR.RequestID.
// stale_tip and result_mismatch are *vcs.RefusedError codes the caller
// re-runs from Prepare.
func (b Backend) Land(ctx context.Context, p vcs.Prepared) (vcs.Landed, error) {
	if p.New == "" {
		return vcs.Landed{}, fmt.Errorf("enth %s: land of MR %s without a dry run's result", b.Repo.Repo, p.MR.ID)
	}
	rec, err := b.LandExpect(ctx, p.MR.Target, p.MR.Changes, p.Tip, p.New, p.MR.RequestID)
	if err != nil {
		return vcs.Landed{}, err
	}
	return vcs.Landed{Record: &rec}, nil
}

// Cleanup has nothing to do on enth: there is no branch to delete (R6, R20,
// R21), and the gate checkout is the caller's directory.
func (Backend) Cleanup(context.Context, vcs.MR) error { return nil }

// FullMessages returns each id's whole message, subject included: what the
// closing-ref gate reads (R12), as git's %B.
func (b Backend) FullMessages(ctx context.Context, ids []string) ([]vcs.Message, error) {
	var msgs []vcs.Message
	for _, id := range ids {
		l, err := b.show(ctx, id)
		if err != nil {
			return msgs, err
		}
		m, err := decodeB(l.Message)
		if err != nil {
			return msgs, err
		}
		msgs = append(msgs, vcs.Message{Rev: id, Body: m})
	}
	return msgs, nil
}

// RemoveGateDir removes a gate checkout the refinery owns. A directory
// holding a .git is refused: whatever it is, it is not a gate checkout, and a
// wrong path must never take a git tree with it. A half-written checkout (a
// crash mid-checkout leaves no .enth/checkout) is removed.
func RemoveGateDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return fmt.Errorf("%s holds a .git: it is not an enth gate checkout, refusing to remove it", dir)
	}
	return os.RemoveAll(dir)
}
