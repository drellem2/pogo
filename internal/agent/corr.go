package agent

import (
	"log"
	"path/filepath"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// Mail -> work item correlation (drellem2/pogo#307).
//
// A work item filed from a mail can carry the mail's id as a TAG, and pogod
// copies that value onto the item's lifecycle events as `details.corr`, so a
// reader can join a mail (macguffin's mail.sent / mail.read, pogod's
// wake_pointer_sent) to the spawn and exit of the worker it caused without
// guessing.
//
// WHICH tag is configuration, not code: `[events] corr_tag_prefix`. The shipped
// default is "mail:" because the shipped coordinator prompt tags mail-filed
// items `mail:<MSG-ID>`, but pogod knows nothing about mail here — it reads
// whatever prefix the install configured, and with none configured (the
// registry's zero value) it emits no `corr` at all. Absent tag, unreadable
// store, unknown item: no `corr`. Never a wrong one.

// SetCorrTagPrefix sets the work-item tag prefix whose value becomes the
// `corr` detail on agent lifecycle events. "" (the zero value) disables it.
func (r *Registry) SetCorrTagPrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.corrTagPrefix = prefix
}

func (r *Registry) getCorrTagPrefix() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.corrTagPrefix
}

// corrForItem returns the correlation id for workItemID under the configured
// prefix, or "" when the mapping is unset, the id is empty, or the item cannot
// be read or carries no matching tag. It is a filesystem read of one item —
// the same read the dispatch gates already make on this path — and it fails
// open to "no corr": correlation is a record, never a reason to refuse.
func (r *Registry) corrForItem(workItemID string) string {
	prefix := r.getCorrTagPrefix()
	if prefix == "" || workItemID == "" {
		return ""
	}
	root := macguffinStoreRoot(r.corrStoreRoot)
	if root == "" {
		return ""
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), workItemID)
	if err != nil {
		log.Printf("corr: could not read work item %s from %s: %v — its events carry no corr", workItemID, root, err)
		return ""
	}
	if !found {
		return ""
	}
	return config.CorrFromTags(item.TagList(), prefix)
}

// addCorr sets details["corr"] when corr is non-empty, and leaves details
// untouched otherwise, so an uncorrelated event is byte-identical to one
// written before drellem2/pogo#307.
func addCorr(details map[string]any, corr string) {
	if corr != "" {
		details["corr"] = corr
	}
}
