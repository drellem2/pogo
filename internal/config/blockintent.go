package config

import "strings"

// BlockTagPrefix is the tag idiom agents invented to say what the assignee field
// could not express before mg-6fb0 taught it the `blocked:<agent>` shape:
// `blocked-on-daniel`, `blocked-on-daniel-confirm`, `blocked-on-redeploy`,
// `blocked-on-mg-01f7`. It is a HUMAN-FACING marker and it stays one — the gate
// reads `assignee`, and only `assignee`.
//
// That is a deliberate refusal, not an omission. Moving the gate onto tags would
// split it across two channels, and the property worth keeping is that
// `mg list --assignee=…` remains the single answerable question about whether an
// item is dispatchable. So this prefix is read for ADVICE, never for gating.
const BlockTagPrefix = "blocked-on-"

// BlockTag returns the first `blocked-on-*` tag in tags, if any. Matching is
// case-insensitive and whitespace-trimmed; the tag is returned as written.
func BlockTag(tags []string) (string, bool) {
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if len(t) >= len(BlockTagPrefix) && strings.EqualFold(t[:len(BlockTagPrefix)], BlockTagPrefix) {
			return t, true
		}
	}
	return "", false
}

// BlockIntentMismatch reports an item that DECLARES a block in the tag channel
// while its assignee leaves it dispatchable — the old idiom, still in use, with
// the gate unable to hear it. It returns the offending tag alongside the verdict
// so the advice can quote what the author actually wrote.
//
// This is the write-time-intent check mg-6fb0 asked for, narrowed to where the
// intent is knowable. The ticket's requirement was written as "fire on
// `--assignee=<some-agent>` on an available item", but the architect's ruling
// corrected the diagnosis underneath it: an item OWNED by mayor and not gated
// *is* dispatchable, and that is correct by design. pm-template files every
// ticket with `--assignee=pm-<name>`, so a check on any named assignee would fire
// on nearly the whole queue and mean nothing. What separates "owner" from
// "intended block" is that the author said so — and when they say so, today, they
// say it in a tag, because that was the only channel available.
//
// Hence the exact predicate: the item declares a block AND is not gated. It
// catches someone still using the old idiom after the shape exists, which is the
// job the ruling assigned this check; and it is measurable rather than
// speculative — mg-a96c carries `assignee: pm-pogo` with
// `blocked-on-daniel-confirm` and is exactly the shape this fires on.
//
// It is ADVICE. Nothing refuses on it, because a tag is not a gate.
func BlockIntentMismatch(assignee string, tags []string, gates []string) (string, bool) {
	if IsDispatchGated(assignee, gates) {
		return "", false
	}
	return BlockTag(tags)
}

// SuggestBlockedAssignee renders the assignee value that would express what a
// `blocked-on-<who>` tag is trying to say, e.g. "blocked:daniel". It returns ""
// when the tag names no one, or names a work item (`blocked-on-mg-1234`) —
// waiting on another ITEM is what `mg new --depends` already expresses, and
// pointing someone at the assignee field for it would be wrong advice.
func SuggestBlockedAssignee(tag string) string {
	t := strings.TrimSpace(tag)
	if len(t) < len(BlockTagPrefix) || !strings.EqualFold(t[:len(BlockTagPrefix)], BlockTagPrefix) {
		return ""
	}
	who := strings.TrimSpace(t[len(BlockTagPrefix):])
	if who == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(who), "mg-") {
		return ""
	}
	return BlockedAssigneePrefix + strings.ToLower(who)
}
