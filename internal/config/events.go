package config

import "strings"

// DefaultCorrTagPrefix is the `[events] corr_tag_prefix` value the shipped
// ("vanilla") configuration carries (drellem2/pogo#307). It is a DEFAULT, not
// a rule pogod enforces: the shipped coordinator prompt files a work item from
// mail with the tag `mail:<MSG-ID>`, and this default makes pogod read that tag
// back as the item's correlation id. Another install sets its own prefix, or
// sets `corr_tag_prefix = ""` to turn correlation off, with no code change.
// Nothing outside config.Load names this constant.
const DefaultCorrTagPrefix = "mail:"

// EventsConfig is the `[events]` section: settings for what pogod writes into
// the event spine (events.log) rather than for any watcher.
type EventsConfig struct {
	// CorrTagPrefix maps a work-item tag PREFIX to the `corr` detail
	// (drellem2/pogo#307). When a polecat's work item carries a tag
	// `<CorrTagPrefix><value>`, pogod adds `details.corr = <value>` to that
	// item's agent_spawn_requested, agent_spawned, agent_stopped,
	// agent_crashed and agent_restarted events.
	//
	// Empty means UNSET: no event carries `corr` at all. Default
	// DefaultCorrTagPrefix.
	CorrTagPrefix string

	// CorrIsMsgID (`corr_is_msg_id`) states that the values of tags under
	// CorrTagPrefix are mail MSG-IDs. Only then can wake_pointer_sent carry
	// `details.corr` equal to its msg_id and have it join the workers' corr
	// (drellem2/pogo#310). pogod cannot know what an install's tags mean, so
	// this is the install's claim, not something pogod infers from the prefix.
	//
	// Default true, paired with DefaultCorrTagPrefix, because the shipped
	// coordinator prompt tags items `mail:<MSG-ID>`. A config layer that sets
	// corr_tag_prefix without also setting corr_is_msg_id resets it to false
	// (see config.Load), so moving to another prefix never leaves the
	// pointer's corr asserting the mail convention.
	CorrIsMsgID bool
}

// CorrEnabled reports whether a tag->corr mapping is configured.
func (c EventsConfig) CorrEnabled() bool { return c.CorrTagPrefix != "" }

// PointerCorr reports whether wake_pointer_sent carries `corr` (= its
// msg_id): only while a mapping is configured AND its tag values are
// declared to be MSG-IDs. Otherwise the pointer's corr could never equal a
// worker's corr, so it is left out rather than written wrong.
func (c EventsConfig) PointerCorr() bool { return c.CorrEnabled() && c.CorrIsMsgID }

// CorrFromTags returns the correlation id carried by tags under prefix: the
// value after prefix on the FIRST tag that starts with it and has a non-empty
// value. It returns "" when prefix is empty (the mapping is unset) or no tag
// matches — never a guessed value. A tag that is exactly the prefix (an empty
// value) is skipped rather than reported as an empty correlation.
func CorrFromTags(tags []string, prefix string) string {
	if prefix == "" {
		return ""
	}
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, prefix); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
