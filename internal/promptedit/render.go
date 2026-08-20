package promptedit

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// shortHash trims a hash for the human line. The full values are in the JSON
// and in the reproduction commands; a 64-hex column makes the report unreadable
// and nobody compares them by eye anyway.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// stampNote describes what KIND of evidence a stamp is. A v0 stamp records one
// hash and its body hash is inferred from it, which is sound (the installer
// writes body == embed) but is not the same as having observed the body being
// written. A reading should say which it is rather than presenting both as the
// same measurement.
func (e Entry) stampNote() string {
	switch {
	case !e.Stamped:
		return "no stamp"
	case e.StampVersion == 0:
		return "v0 stamp (body hash inferred from the single recorded hash)"
	default:
		return "v1 stamp"
	}
}

// Render writes the whole report: findings first, then the classified census.
//
// THE CENSUS IS ALWAYS PRINTED, clean or not, and each bucket is named with its
// reason. A detector that printed only its findings would read as though it had
// judged everything it enumerated — and here most of what it enumerates is
// deliberately unjudged, so that reading would be false about the majority of
// the corpus. This is the same posture `pogo check-prompts` takes with the flag
// values it cannot decide.
func (r Report) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "prompt hand-edit sweep of %s\n", r.Root)
	fmt.Fprintf(&b, "%d installed corpus files enumerated against %d shipped paths\n\n", r.Total(), r.ShippedPaths)

	if len(r.Findings) == 0 {
		fmt.Fprintf(&b, "no hand-edits: %d file(s) judged, all match their own stamp\n", len(r.Clean)+len(r.Findings))
	} else {
		fmt.Fprintf(&b, "%d HAND-EDITED file(s) — the stamp records a body this file no longer has:\n\n", len(r.Findings))
		for _, f := range r.Findings {
			fmt.Fprintf(&b, "  %s\n", f.Path)
			fmt.Fprintf(&b, "      agent:    %s%s\n", f.Agent, ownerSuffix(f.Owned))
			fmt.Fprintf(&b, "      stamp:    body=%s  (%s)\n", shortHash(f.RecordedHash), f.stampNote())
			fmt.Fprintf(&b, "      actual:   body=%s\n", shortHash(f.ActualHash))
			fmt.Fprintf(&b, "      reproduce: head -1 %s\n", filepath.Join(r.Root, filepath.FromSlash(f.Path)))
			fmt.Fprintf(&b, "                 tail -n +2 %s | shasum -a 256\n", filepath.Join(r.Root, filepath.FromSlash(f.Path)))
		}
		fmt.Fprintf(&b, "\n%d of %d judged file(s) match their stamp\n", len(r.Clean), len(r.Clean)+len(r.Findings))
	}

	if len(r.Unreadable) > 0 {
		// Not a census line. An unreadable prompt is a prompt whose content is
		// unknown, and unknown is not the same as matching.
		fmt.Fprintf(&b, "\n%d corpus file(s) COULD NOT BE READ — their state is unknown, not clean:\n", len(r.Unreadable))
		for _, p := range r.Unreadable {
			fmt.Fprintf(&b, "    %s\n", p)
		}
	}

	b.WriteString("\n" + r.renderCensus())
	return b.String()
}

// renderCensus prints the out-of-domain buckets, each under its own reason.
func (r Report) renderCensus() string {
	var b strings.Builder
	fmt.Fprintf(&b, "OUT OF DOMAIN — %d file(s) enumerated and deliberately NOT judged.\n", len(r.OutOfDomain))
	b.WriteString("An unstamped file is ambiguous between \"by design, no upstream\" and \"lost its\n" +
		"stamp\", so each is listed under the reason it could not be read, never pooled\n" +
		"into one \"unknown\" count. Reporting them as findings would be false positives;\n" +
		"waving them through would let a lost stamp hide among them.\n")

	for _, spec := range censusOrder {
		items := r.OutOfDomainBy(spec.reason)
		fmt.Fprintf(&b, "\n  %s — %d file(s)\n", spec.reason, len(items))
		for _, line := range strings.Split(spec.why, "\n") {
			fmt.Fprintf(&b, "      %s\n", line)
		}
		for _, o := range items {
			note := ""
			if o.Reason == ReasonUpstreamWithdrawn {
				// State what the stamp says even though this is not a finding.
				// Declining to raise it is a judgement about ACTIONABILITY, not
				// a claim that the file is unchanged.
				if o.Edited() {
					note = fmt.Sprintf("  [stamp says EDITED: %s → %s]", shortHash(o.RecordedHash), shortHash(o.ActualHash))
				} else {
					note = "  [stamp still matches]"
				}
			}
			fmt.Fprintf(&b, "        %s%s\n", o.Path, note)
		}
	}
	return b.String()
}

// censusOrder fixes the order the buckets print in and carries the one-line
// reason each exists. It is a slice rather than a map so two runs over
// unchanged input read identically.
var censusOrder = []struct {
	reason OutOfDomainReason
	why    string
}{
	{ReasonStampMissing,
		"The shipped corpus HAS this path, so the installer would have stamped it,\n" +
			"and the stamp is not there. Unjudgeable — and the absence is the one\n" +
			"unstamped case that is worth a reader's attention."},
	{ReasonUpstreamWithdrawn,
		"Stamped by an install, but the corpus no longer ships this path. The stamp\n" +
			"still reads, and what it says is printed below — but there is no upstream\n" +
			"to reconcile against, so raising it would ask for work nobody can do."},
	{ReasonNoUpstream,
		"No upstream ships this path and it carries no stamp: the deployed file IS\n" +
			"the source. \"Hand-edited since it shipped\" is not a meaningful question\n" +
			"to ask of it. Expected, and normally the largest bucket."},
}

func ownerSuffix(owned bool) string {
	if owned {
		return " (owns this prompt)"
	}
	return " (fallback: no running agent owns this file)"
}

// Recipients groups the findings by the agent that can act on them, so the
// runner sends one mail per agent rather than one per file.
//
// Returned sorted by agent name, and each agent's findings sorted by path, so
// two runs over an unchanged corpus produce byte-identical mail — which is what
// makes the fingerprint suppression in watcher.go mean anything.
func (r Report) Recipients() []Recipient {
	byAgent := map[string][]Finding{}
	for _, f := range r.Findings {
		byAgent[f.Agent] = append(byAgent[f.Agent], f)
	}
	var out []Recipient
	for name, fs := range byAgent {
		sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
		out = append(out, Recipient{Agent: name, Findings: fs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// Recipient is one agent and the findings addressed to it.
type Recipient struct {
	Agent    string
	Findings []Finding
}

// Owned reports whether every finding for this agent is a prompt it owns. Used
// only to word the subject line.
func (rc Recipient) Owned() bool {
	for _, f := range rc.Findings {
		if !f.Owned {
			return false
		}
	}
	return len(rc.Findings) > 0
}

// Subject is the mail subject for one recipient.
func (rc Recipient) Subject() string {
	if len(rc.Findings) == 1 {
		f := rc.Findings[0]
		if f.Owned {
			return fmt.Sprintf("[prompt-edit] YOUR prompt %s has been edited since it was installed", f.Path)
		}
		return fmt.Sprintf("[prompt-edit] %s has been edited since it was installed", f.Path)
	}
	whose := ""
	if rc.Owned() {
		whose = "your "
	}
	return fmt.Sprintf("[prompt-edit] %d %sprompts have been edited since they were installed", len(rc.Findings), whose)
}

// Body writes the notice for one recipient.
//
// It states the evidence, hands over the two commands that reproduce it, and
// asks for a JUDGEMENT rather than issuing a command. That is not politeness:
// the only safe repair either re-runs the installer (discarding the edit) or
// carries the local line forward and re-stamps — and re-stamping requires the
// installer's exact canonicalisation, which this detector does not have. A
// paste-ready one-liner here would be a one-liner that either destroys the edit
// or certifies a body nothing validated.
func (rc Recipient) Body(root string, r Report) string {
	var b strings.Builder

	plural := "This prompt file has"
	if len(rc.Findings) > 1 {
		plural = fmt.Sprintf("%d prompt files have", len(rc.Findings))
	}
	fmt.Fprintf(&b, "%s been changed in place since pogo installed %s.\n\n",
		plural, map[bool]string{true: "it", false: "them"}[len(rc.Findings) == 1])

	for _, f := range rc.Findings {
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		fmt.Fprintf(&b, "  %s\n", abs)
		fmt.Fprintf(&b, "      stamp records body sha256:%s   (%s)\n", f.RecordedHash, f.stampNote())
		fmt.Fprintf(&b, "      the body now hashes to    %s\n\n", f.ActualHash)
	}

	b.WriteString("HOW THIS WAS DETECTED, and how to check it yourself. Every deployed prompt with\n" +
		"an upstream carries a self-describing stamp on its first line recording the hash\n" +
		"of the body the installer wrote. Two commands reproduce the reading, with no\n" +
		"reference checkout and no .dist sidecar involved:\n\n")
	for _, f := range rc.Findings {
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		fmt.Fprintf(&b, "    head -1 %s\n", abs)
		fmt.Fprintf(&b, "    tail -n +2 %s | shasum -a 256\n", abs)
	}

	b.WriteString("\nWHAT YOU ARE BEING ASKED. Nothing has been repaired and nothing will be: this is\n" +
		"a detector, deliberately. Carrying a local line forward changes the body, which\n" +
		"stales the stamp, and the stamp cannot be recomputed without the installer's\n" +
		"exact canonicalisation — a tool that recomputed it anyway would silently certify\n" +
		"a body it never validated. You are the only party who can say whether the edit\n" +
		"is still load-bearing.\n\n" +
		"If it is, keep it and expect this notice again on the renotify interval; the\n" +
		"file will keep reading as edited until an install rewrites it. If it is not —\n" +
		"a stray save, a debugging line, an experiment left behind — `pogo install` will\n" +
		"restore the shipped text, and a --force install writes a .bak sidecar first.\n\n" +
		"A DECLINED SYNC IS A DIFFERENT NOTICE. If you also have mail from\n" +
		"pogod-promptsync about a .dist sidecar, that one is about a shipped update that\n" +
		"could not be applied because of these very edits. Reconciling that resolves\n" +
		"both; this notice alone means the edits exist and no shipped update has collided\n" +
		"with them yet — which is the case nothing used to report (mg-0c96).\n\n")

	fmt.Fprintf(&b, "SCOPE OF THE SWEEP THAT FOUND THIS. %d installed corpus files under %s were\n"+
		"enumerated; %d were judged against their stamp and %d were deliberately not\n"+
		"judged, because an unstamped file with no upstream is not a defect. Full\n"+
		"classification, including which files were left unjudged and why:\n\n"+
		"    pogo check-prompt-edits\n",
		r.Total(), root, len(r.Clean)+len(r.Findings), len(r.OutOfDomain))

	return b.String()
}
