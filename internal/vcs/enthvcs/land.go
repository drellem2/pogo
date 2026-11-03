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

// Landing on enth (docs/design/enth-vcs.md §6, mg-5b904).
//
// An enth repo has no merge queue. The worker lands its own work:
//
//	enth land C1…Cn --request-id <item>/<attempt>
//
// and hands enth's `landed` line to `pogo land --item`, which checks it
// against enthd and closes the work item exactly as a refinery merge closes
// one. The refinery's own enth land path (slice 7: dry run, gate the result,
// --expect-tip/--expect-new) was deleted with that decision, so this backend
// is no longer a Lander: Prepare, Land and Cleanup refuse, and the refinery
// refuses an enth MR before it would reach them.
//
// The request id is what makes a worker's land safe to repeat: the same id
// and Changes again answer what the first land answered and land nothing
// (measured 2026-11-03 on enth 0.2.0-dev). A land of already-landed Changes
// under a NEW id is not refused — it lands them a second time (enth bug
// mg-19949) — so a land whose answer was lost is re-issued with its ORIGINAL
// id, never a new one. The polecat template's self_land function keeps that
// rule.

// ErrSelfLanded is the refusal of every Lander step on enth: the worker lands
// its own work, and the refinery does not.
var ErrSelfLanded = errors.New("enth repos are self-landed: the worker runs `enth land <stack> --request-id <item>/<attempt>` and records it with `pogo land --item=<item>` (enth-vcs §6); the refinery does not land enth work")

// Prepare refuses: see ErrSelfLanded.
func (b Backend) Prepare(context.Context, vcs.MR) (vcs.Prepared, error) {
	return vcs.Prepared{}, fmt.Errorf("enth %s: %w", b.Repo.Repo, ErrSelfLanded)
}

// Land refuses: see ErrSelfLanded.
func (b Backend) Land(context.Context, vcs.Prepared) (vcs.Landed, error) {
	return vcs.Landed{}, fmt.Errorf("enth %s: %w", b.Repo.Repo, ErrSelfLanded)
}

// Cleanup has nothing to do on enth: there is no branch to delete.
func (Backend) Cleanup(context.Context, vcs.MR) error { return nil }

// RefusalCode is enth's stable error code carried by err, or "" when err is
// not an enth refusal (a failed exec, an unreadable answer).
func RefusalCode(err error) string {
	var re *vcs.RefusedError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// landLine is `enth land`'s stdout line: dry_run, landed or conflict.
type landLine struct {
	Type    string             `json:"type"`
	Ref     string             `json:"ref"`
	Entry   int                `json:"entry"`
	Old     *string            `json:"old"`
	New     string             `json:"new"`
	Changes []vcs.LandedChange `json:"changes"`
}

// ParseLanded reads a successful `enth land`'s stdout: its one `landed` line,
// as the land record of §7. Anything else — a dry run, a conflict, no line,
// two landed lines — is an error: only a land that happened is a record.
func ParseLanded(out []byte) (vcs.LandRecord, error) {
	var rec vcs.LandRecord
	found := 0
	for _, raw := range strings.Split(string(out), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var l landLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			return vcs.LandRecord{}, fmt.Errorf("enth land: unreadable line %q: %v", raw, err)
		}
		switch l.Type {
		case "landed":
			found++
			rec = vcs.LandRecord{Ref: l.Ref, Entry: l.Entry, New: l.New, Changes: l.Changes}
			if l.Old != nil {
				rec.Old = *l.Old
			}
		case "conflict":
			return vcs.LandRecord{}, fmt.Errorf("enth land reported a conflict, so nothing landed: %s", raw)
		default:
			return vcs.LandRecord{}, fmt.Errorf("enth land: a %q line is not a land (%s)", l.Type, raw)
		}
	}
	switch {
	case found == 0:
		return vcs.LandRecord{}, fmt.Errorf("enth land: no landed line in %q", bytes.TrimSpace(out))
	case found > 1:
		return vcs.LandRecord{}, fmt.Errorf("enth land: %d landed lines; one land is one line", found)
	case rec.New == "" || len(rec.Changes) == 0:
		return vcs.LandRecord{}, fmt.Errorf("enth land: the landed line names no result or no Changes: %q", bytes.TrimSpace(out))
	}
	return rec, nil
}

// LandRef is the full ref a land onto target moves: the trunk for "" or the
// trunk's short name, refs/heads/<target> otherwise.
func (b Backend) LandRef(target string) string {
	return b.ResolveBase(context.Background(), vcs.Repo{}, target)
}

// FullMessages returns each id's whole message, subject included: what the
// closing-ref check reads (R12), as git's %B.
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
