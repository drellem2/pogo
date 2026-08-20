package agent

// The stamp READER, exported (mg-0c96).
//
// The stamp InstallPrompts writes is self-describing: it records the hash of
// the body it wrote beside the body itself, so a later disagreement between the
// two IS the signature of an in-place edit. That signature was verified by hand
// on 2026-08-20 (mg-0635) with two shell commands per file:
//
//	head -1 <prompt>
//	tail -n +2 <prompt> | shasum -a 256
//
// and nothing in the tree read it on a cadence. internal/promptedit is the
// detector that does; this file is the parser it reads through, kept HERE next
// to stampedContent rather than reimplemented there for the same reason
// StripPromptStamp is exported next to the stripper: the definition of what a
// stamp looks like must have exactly one home, or the reader and the writer
// drift and the drift reads as a finding.

import (
	"path/filepath"
	"strings"
)

// Prompt stamp versions, as reported by ReadPromptStamp.
const (
	// PromptStampV1 is the current shape: "embed=sha256:<hex> body=sha256:<hex>".
	// Both hashes are recorded, so BodyHash is a direct observation of what was
	// written.
	PromptStampV1 = 1
	// PromptStampV0 is the legacy shape: "pogo-prompt-hash: <hex>", one hash
	// only. BodyHash is INFERRED from it — see PromptStamp.BodyHash — which is
	// sound because InstallPrompts writes body == embed, but it is a different
	// kind of evidence and a reader that reports on it should say so.
	PromptStampV0 = 0
)

// PromptStamp is a parsed pogo-prompt stamp from an installed prompt or config
// file's first line.
type PromptStamp struct {
	// EmbedHash is the hash of the embed payload that produced the file.
	EmbedHash string
	// BodyHash is the hash of the file body as it was written at install time.
	// For a v0 stamp this is the embed hash, which equals the body hash at
	// install by construction; Version says which you are looking at.
	BodyHash string
	// Version is PromptStampV1 or PromptStampV0.
	Version int
}

// ReadPromptStamp parses the stamp from the first line of an installed prompt
// or config file's contents, in either comment flavor and either version.
//
// The second return is whether a stamp was found at all, and callers MUST
// branch on it rather than on an empty hash. "No stamp" and "a stamp recording
// an empty hash" are different states, and collapsing them is how the file with
// no upstream and the file that LOST its stamp become indistinguishable — the
// ambiguity internal/promptedit exists to keep apart.
func ReadPromptStamp(data []byte) (PromptStamp, bool) {
	firstLine, _, _ := strings.Cut(string(data), "\n")

	stampSuffix := strings.TrimSuffix(promptStampSuffix, "\n")
	if strings.HasPrefix(firstLine, promptStampPrefix) && strings.HasSuffix(firstLine, stampSuffix) {
		body := strings.TrimSuffix(strings.TrimPrefix(firstLine, promptStampPrefix), stampSuffix)
		s := parsePromptStampBody(body)
		return PromptStamp{EmbedHash: s.EmbedHash, BodyHash: s.BodyHash, Version: PromptStampV1}, true
	}
	if strings.HasPrefix(firstLine, promptStampPrefixTOML) {
		s := parsePromptStampBody(strings.TrimPrefix(firstLine, promptStampPrefixTOML))
		return PromptStamp{EmbedHash: s.EmbedHash, BodyHash: s.BodyHash, Version: PromptStampV1}, true
	}

	hashSuffix := strings.TrimSuffix(promptHashSuffix, "\n")
	if strings.HasPrefix(firstLine, promptHashPrefix) && strings.HasSuffix(firstLine, hashSuffix) {
		h := strings.TrimSuffix(strings.TrimPrefix(firstLine, promptHashPrefix), hashSuffix)
		return PromptStamp{EmbedHash: h, BodyHash: h, Version: PromptStampV0}, true
	}
	if strings.HasPrefix(firstLine, promptHashPrefixTOML) {
		h := strings.TrimPrefix(firstLine, promptHashPrefixTOML)
		return PromptStamp{EmbedHash: h, BodyHash: h, Version: PromptStampV0}, true
	}

	return PromptStamp{}, false
}

// PromptBodyHash returns the hash of data with any leading stamp line stripped
// — i.e. the value stampedContent would have recorded had it written this file.
// It is the other half of the comparison ReadPromptStamp enables, and it uses
// the same contentHash the writer does so the two cannot disagree about
// encoding.
func PromptBodyHash(data []byte) string {
	return contentHash(stripPromptHashStamp(data))
}

// PromptAddressee resolves the corpus-relative path of a prompt to the agent
// that can act on it, and reports whether that agent OWNS the prompt or is
// merely the fallback.
//
// The mapping mirrors ListPrompts, which is the authority on how a file under
// ~/.pogo/agents/ becomes an agent:
//
//   - mayor.md      → the CONFIGURED coordinator name. The file is always
//     mayor.md (mechanism) but the agent it starts as follows
//     [agents] coordinator (policy), so hardcoding "mayor"
//     here would misroute on any consumer who renamed it.
//   - crew/<n>.md   → <n>. This is the same string the agent checks mail as,
//     because it is the same string ListPrompts and autostart
//     name it by.
//   - anything else → the coordinator, owned=false. templates/polecat*.md and
//     pm/pm-template.md are consumed at spawn and belong to
//     no running agent, so there is no inbox to address; the
//     coordinator is who dispatches from them.
//
// NEVER GUESS A NAME. Mail to a name no agent reads is silently accepted into a
// phantom mailbox and lost. Every branch above returns either a name taken from
// the prompt tree itself or the configured coordinator — never a name
// synthesized from a path we did not recognize.
//
// It lives here, beside ListPrompts, because two callers need it: pogod's
// declined-sync notifier (cmd/pogod/promptsyncnotify.go, mg-c3f0) and the
// hand-edit detector (internal/promptedit, mg-0c96). A second copy of a routing
// table is a second chance to misroute.
func PromptAddressee(rel, coordinator string) (to string, owned bool) {
	clean := filepath.ToSlash(filepath.Clean(rel))
	switch {
	case clean == "mayor.md":
		return coordinator, true
	case strings.HasPrefix(clean, "crew/") && strings.HasSuffix(clean, ".md"):
		name := strings.TrimSuffix(strings.TrimPrefix(clean, "crew/"), ".md")
		// A nested path under crew/ is not a crew agent, and neither is an
		// empty stem. Fall back rather than address a name we invented.
		if name == "" || strings.Contains(name, "/") {
			return coordinator, false
		}
		return name, true
	default:
		return coordinator, false
	}
}
