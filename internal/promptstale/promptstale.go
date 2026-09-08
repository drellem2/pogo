// Package promptstale is the RUNNER for the prompt-corpus staleness witness
// (mg-385f): it rides pogod's heartbeat, compares the installed prompt tree
// against the corpus a git ref ships, and mails the agent that is reading each
// superseded file.
//
// # The detector already existed and nothing ran it
//
// internal/staleness answers the question exactly (mg-dd49): it hashes each
// installed prompt body against the same path at a git ref, in both directions,
// and it deliberately never consults this binary's own embed — which is what
// lets a stale install still raise the alarm about itself. `pogo check-staleness`
// prints that answer, correctly, to whoever types it.
//
// Nobody typed it. On 2026-09-06 the coordinator discovered by hand that its own
// deployed mayor.md was 15 days behind source and that the missing section was a
// warning against an instrument it had run that morning. Two days later, on
// 2026-09-08, the same three files were still stale — measured from this branch,
// against the live tree:
//
//	mayor.md                     installed 1642 lines, ref 1771 — behind by 129
//	pm/pm-template.md            installed 1036 lines, ref 1044 — behind by 8
//	templates/polecat-triage.md  installed  268 lines, ref  272 — behind by 4
//
// That is mg-10e3's shape one level down, and mg-385f's own thesis applied to
// the fix for mg-385f: a correct detector reporting into a surface whose read
// cadence is "when somebody thinks to type it". This package is the reader on a
// cadence, and it is the same posture internal/promptedit took for the hand-edit
// half — same heartbeat, same per-agent routing, same report-only constraint.
//
// # Why none of the three existing prompt alarms covers this
//
// All three are real, all three fire on this box, and each is blind to the
// condition above by construction:
//
//	agent.CheckPromptDrift        compares the installed corpus against THIS
//	(`pogo doctor --check` 5b)    binary's EMBEDDED copy. A missed redeploy
//	                              stales both together, so they match and it
//	                              passes — truthfully, about a different
//	                              question. And doctor has no scheduled runner.
//	promptsyncnotify (mg-c3f0)    fires when InstallPrompts DECLINES a shipped
//	                              update. It needs the installer to have run.
//	                              pogod installs prompts at boot, so on a host
//	                              whose daemon has not restarted since the last
//	                              deploy it cannot fire at all — which is
//	                              precisely the state that produces staleness.
//	promptedit (mg-0c96)          compares each file against ITS OWN stamp. A
//	                              prompt installed cleanly in August and never
//	                              touched since matches its stamp perfectly. It
//	                              reports a 129-line-stale mayor.md as CLEAN,
//	                              and it is right to: hand-edited and superseded
//	                              are different facts.
//
// # What it does NOT do, and why each omission is deliberate
//
// IT DOES NOT FETCH. staleness.PromptOptions.Fetch exists and stays off here. A
// detector that mutates the tree it judges has made itself a participant, the
// fetch would overwrite the FETCH_HEAD timestamp the reference's own age is read
// from, and on this host it would race the nightly deploy for the same checkout.
// The cost is that the verdict is "the fleet matches what was DEPLOYED", not
// "what shipped" — so the reference's commit, its fetch age and whether the live
// remote has moved past it all travel in every notice. `pogo check-staleness
// --fetch` is the manual escalation.
//
// IT DOES NOT ALARM ON A REFERENCE THAT IS ITSELF BEHIND. The remote head is
// queried (read-only, bounded) and reported, never used as the firing predicate:
// origin moves all day and a notice on every push would be filtered within a
// week. A frozen reference is the deploy half's finding — staleness.CheckDeploy
// and driftwatch's no-fire check already own "the nightly did not run" — and
// duplicating it here would mail two agents about one outage.
//
// IT DOES NOT REPAIR. There is no seam through which this package could install
// a prompt, and adding one would be wrong for a reason particular to this
// condition rather than as a general convention: the fix is a redeploy, which
// restarts agents, and choosing when a running coordinator is restarted is not a
// judgement a detector gets to make on its own schedule.
package promptstale

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/staleness"
)

// Finding kinds. They are the three ways a shipped prompt can fail to be what
// the fleet is reading, and they are kept apart because the remedies differ in
// urgency even though all three end at a redeploy.
const (
	// KindDiffers: both sides have the file and the bodies disagree. The
	// ordinary case, and the one that produced this ticket.
	KindDiffers = "differs"
	// KindNotInstalled: the ref ships the path and the live tree has no copy.
	// Worse than differing — an agent spawned from a missing template falls
	// back to whatever the caller does without one.
	KindNotInstalled = "not-installed"
	// KindUnreadable: a corpus-shaped installed file that could not be read.
	// Reported as a finding rather than a census line for the reason the whole
	// check-* family states: unknown is not the same as matching, and a sweep
	// that quietly dropped it would report "clean" over an unexamined file.
	KindUnreadable = "unreadable"
)

// Finding is one corpus path the fleet is not reading at the shipped revision,
// addressed to the agent that can act on it.
type Finding struct {
	// Path is corpus-relative and slash-separated ("templates/polecat.md").
	Path string `json:"path"`
	// Kind is one of the Kind* constants above.
	Kind string `json:"kind"`
	// Agent is who this is mailed to, and Owned reports whether that agent OWNS
	// the prompt or is the coordinator standing in for a file no running agent
	// owns. Resolved through agent.PromptAddressee — the same routing table the
	// declined-sync notifier and the hand-edit detector use, so a rename cannot
	// misroute one of the three and not the others.
	Agent string `json:"agent"`
	Owned bool   `json:"owned"`

	ShippedHash    string `json:"shipped_hash,omitempty"`
	InstalledHash  string `json:"installed_hash,omitempty"`
	ShippedLines   int    `json:"shipped_lines,omitempty"`
	InstalledLines int    `json:"installed_lines,omitempty"`
}

// Fingerprint is what the suppression store remembers, and it deliberately
// carries BOTH sides.
//
// Keying on the shipped hash alone would be enough to re-notify when the repo
// moves on, but it would stay silent when the INSTALLED side changes under a
// fixed ref — a partial install, a hand-edit, a half-finished reconciliation.
// In each of those the recipient's job is now a different job, so the notice is
// news again rather than the same notice repeated.
func (f Finding) Fingerprint() string {
	return f.Kind + ":" + f.ShippedHash + ":" + f.InstalledHash
}

// LineNote describes the size relationship in words. It is commentary on a
// decision already made by hash — including the case worth making unmissable,
// where the two files are the same length and still different.
func (f Finding) LineNote() string {
	switch {
	case f.Kind == KindUnreadable:
		return "could not be read — its content is unknown, not matching"
	case f.Kind == KindNotInstalled:
		return fmt.Sprintf("%d lines shipped, nothing installed", f.ShippedLines)
	case f.InstalledLines == f.ShippedLines:
		return fmt.Sprintf("same length (%d lines), different content", f.ShippedLines)
	case f.InstalledLines > f.ShippedLines:
		return fmt.Sprintf("installed %d lines, ref %d — installed is LONGER by %d",
			f.InstalledLines, f.ShippedLines, f.InstalledLines-f.ShippedLines)
	default:
		return fmt.Sprintf("installed %d lines, ref %d — installed is behind by %d",
			f.InstalledLines, f.ShippedLines, f.ShippedLines-f.InstalledLines)
	}
}

// Report is one sweep's answer: what is superseded, what it was compared
// against, and how much that reference can be trusted.
type Report struct {
	// Root is the installed tree that was read.
	Root string `json:"root"`
	// Reference names the repo, ref, resolved commit and last fetch. It travels
	// with every report and into every mail body: a verdict whose reference a
	// reader cannot see is the shape mg-385f exists to document, and the
	// reference here is a local mirror whose freshness is exactly one of the
	// things that fails.
	Reference staleness.Reference `json:"reference"`
	// Remote judges the REFERENCE against the live remote head. Context, never
	// the firing predicate — see the package doc.
	Remote staleness.RemoteState `json:"remote"`
	// RemoteSkipped records that the caller asked for no remote query at all,
	// which the witness leaves as an UNARMED RemoteState indistinguishable from
	// "this repo has no remote head to compare against". Two different facts
	// about the same missing qualifier, and a reader told the wrong one would go
	// looking for a misconfigured checkout that is fine.
	RemoteSkipped bool `json:"remote_skipped,omitempty"`
	// Shipped is how many paths the ref carries: the denominator, so a zero
	// finding count is readable as a reading rather than as an empty domain.
	Shipped  int       `json:"shipped_files"`
	Findings []Finding `json:"findings"`
	// Unjudged are installed corpus-shaped files the ref does not ship — the
	// crew/pm-*.md stubs, pm/anti-drift-protocol.md and friends. A census, never
	// a finding: the deployed file IS the source for those.
	Unjudged []string `json:"unjudged"`
	// Err is set when the comparison could not be made at all. Distinct from
	// "no findings": a check that could not run has not found the fleet current.
	Err string `json:"error,omitempty"`
	// SelfCeiling is what THIS daemon's embedded corpus carries, measured
	// against the findings above (mg-1e8e).
	//
	// pogod is itself the automatic installer — it calls agent.InstallPrompts
	// at every boot — so this row is the ceiling on the automatic path, and it
	// decides which of two very different notices the recipient should get:
	// "an install would fix this" or "an install here is a no-op and a newer
	// binary has to land AND BE RUN first". Nil when the sweep found nothing,
	// because a ceiling exists to qualify a remedy and a clean sweep prescribes
	// none.
	SelfCeiling *staleness.InstallerCeiling `json:"self_ceiling,omitempty"`
}

// SelfCeilingName labels pogod's own row wherever it is printed. One constant
// rather than two matching string literals: the watcher supplies the source and
// the notice reads it back, and a rename that touched one and not the other
// would silently produce a report about an installer nobody can identify.
const SelfCeilingName = "this pogod"

// ReferenceQualified reports whether the verdict is weaker than it looks —
// the reference has demonstrably not seen everything that shipped.
//
// staleness.RemoteState.Clean() already encodes the judgement (behind with a
// known-zero corpus delta is still clean; behind with an UNKNOWN composition is
// not, because unknown is not clean), and it is reused rather than restated so
// the runner and `pogo check-staleness` cannot come to different conclusions
// about the same reference.
func (r Report) ReferenceQualified() bool { return !r.Remote.Clean() }

// FromStaleness converts the witness's answer into an addressed report.
//
// coordinator must be the CONFIGURED coordinator name. It is not defaulted
// here: every finding on a file no running agent owns is addressed to it, and a
// guessed name delivers the whole report into a phantom mailbox that exists and
// is read by nobody.
func FromStaleness(rep staleness.PromptReport, coordinator string) Report {
	out := Report{
		Root:      rep.InstalledRoot,
		Reference: rep.Reference,
		Remote:    rep.Remote,
		Shipped:   rep.Shipped,
		Unjudged:  rep.Unjudged,
		Err:       rep.Err,
	}
	for _, d := range rep.Deltas {
		to, owned := agent.PromptAddressee(d.Path, coordinator)
		out.Findings = append(out.Findings, Finding{
			Path: d.Path, Kind: d.Kind, Agent: to, Owned: owned,
			ShippedHash: d.ShippedHash, InstalledHash: d.InstalledHash,
			ShippedLines: d.ShippedLines, InstalledLines: d.InstalledLines,
		})
	}
	for _, p := range rep.Unreadable {
		to, owned := agent.PromptAddressee(p, coordinator)
		out.Findings = append(out.Findings, Finding{
			Path: p, Kind: KindUnreadable, Agent: to, Owned: owned,
		})
	}
	sort.Slice(out.Findings, func(i, j int) bool { return out.Findings[i].Path < out.Findings[j].Path })
	// The witness returns one row per supplied source; the runner supplies
	// exactly one, pogod's own embed. Matched BY NAME rather than by index so a
	// second source added later cannot silently be reported as this one.
	for i := range rep.Ceilings {
		if rep.Ceilings[i].Name == SelfCeilingName {
			out.SelfCeiling = &rep.Ceilings[i]
			break
		}
	}
	return out
}

// Recipient is one agent and the findings addressed to it.
type Recipient struct {
	Agent    string
	Findings []Finding
}

// Recipients groups the findings by the agent that reads them, so the runner
// sends one mail per agent rather than one per file.
//
// Sorted by agent and, within an agent, by path — so two sweeps over an
// unchanged fleet produce byte-identical mail, which is what makes the
// fingerprint suppression in watcher.go mean anything.
func (r Report) Recipients() []Recipient {
	byAgent := map[string][]Finding{}
	for _, f := range r.Findings {
		byAgent[f.Agent] = append(byAgent[f.Agent], f)
	}
	out := make([]Recipient, 0, len(byAgent))
	for name, fs := range byAgent {
		sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
		out = append(out, Recipient{Agent: name, Findings: fs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// Owned reports whether every finding for this recipient is a prompt it owns.
// Used only to word the subject line.
func (rc Recipient) Owned() bool {
	for _, f := range rc.Findings {
		if !f.Owned {
			return false
		}
	}
	return len(rc.Findings) > 0
}

// Subject is the mail subject for one recipient.
//
// "YOUR prompt" when the agent owns the file, because the single most expensive
// instance of this class was a coordinator running an instrument its own
// superseded prompt would have told it not to trust. A subject that reads as a
// fleet notice gets triaged as one.
func (rc Recipient) Subject() string {
	if len(rc.Findings) == 1 {
		f := rc.Findings[0]
		if f.Owned {
			return fmt.Sprintf("[prompt-stale] YOUR prompt %s is not the version the repo ships", f.Path)
		}
		return fmt.Sprintf("[prompt-stale] %s is not the version the repo ships", f.Path)
	}
	whose := ""
	if rc.Owned() {
		whose = "your "
	}
	return fmt.Sprintf("[prompt-stale] %d %sprompts are not the version the repo ships", len(rc.Findings), whose)
}

// Body writes the notice for one recipient.
//
// Three things it always states, each because leaving one out is a documented
// failure of this exact lineage:
//
//   - WHAT IT WAS COMPARED WITH, including the reference's own fetch age. "Your
//     prompt is stale" with no named reference is a number nobody can chase.
//   - THAT THE FINDING IS REPRODUCIBLE, with the command. A recipient who cannot
//     re-derive a claim has to either believe it or ignore it.
//   - THAT THE FIX IS A REDEPLOY AND NOT A HAND-EDIT. Editing the deployed copy
//     makes it diverge from source with no expiry and no record, so the next
//     legitimate update is either clobbered silently or declined — trading a
//     known gap for an invisible one.
func (rc Recipient) Body(r Report) string {
	var b strings.Builder

	subject := "This prompt file is"
	if len(rc.Findings) > 1 {
		subject = fmt.Sprintf("%d prompt files are", len(rc.Findings))
	}
	fmt.Fprintf(&b, "%s not the version the repo ships. What you are reading was installed by an\n"+
		"earlier deploy; the corpus has moved since and nothing has installed it here.\n\n", subject)

	for _, f := range rc.Findings {
		fmt.Fprintf(&b, "  %s\n", filepath.Join(r.Root, filepath.FromSlash(f.Path)))
		fmt.Fprintf(&b, "      %-14s %s\n", f.Kind, f.LineNote())
		if f.Kind == KindDiffers {
			fmt.Fprintf(&b, "      shipped body sha256:%s\n", f.ShippedHash)
			fmt.Fprintf(&b, "      installed body      %s\n", f.InstalledHash)
		}
	}

	fmt.Fprintf(&b, "\nWHAT THIS WAS COMPARED WITH. Not this daemon's own embedded copy — a missed\n"+
		"redeploy stales the binary and the prompts together, so that comparison passes\n"+
		"truthfully while the fleet drifts. The reference is a git ref:\n\n")
	fmt.Fprintf(&b, "    repo:      %s\n", r.Reference.Repo)
	fmt.Fprintf(&b, "    ref:       %s = %s\n", r.Reference.Ref, shortSHA(r.Reference.Commit))
	if r.Reference.CommitTime != "" {
		fmt.Fprintf(&b, "    committed: %s\n", r.Reference.CommitTime)
	}
	b.WriteString(referenceAgeLine(r))
	b.WriteString(remoteLine(r))

	b.WriteString("\nREPRODUCE IT. The decision is a sha256 of each file body with the install stamp\n" +
		"stripped, in both directions — length is printed for orientation and decided\n" +
		"nothing. The full report, including the files deliberately not judged:\n\n" +
		"    pogo check-staleness\n" +
		"    pogo check-staleness --fetch    # compare against what has shipped SINCE the deploy\n")

	b.WriteString(selfCeilingLines(rc, r))

	b.WriteString("\nWHAT YOU ARE BEING ASKED. Nothing has been repaired and this detector has no\n" +
		"seam to repair through. The fix is a redeploy, which restarts agents, and when a\n" +
		"running coordinator is restarted is not a call a sweep gets to make on its own\n" +
		"schedule. If you need the shipped text before the next nightly:\n\n" +
		"    pogo agent prompt install     # from a build of the reference above\n" +
		"                                  # read the CEILING block above FIRST — on a\n" +
		"                                  # daemon that has not restarted, this is a no-op\n\n" +
		"DO NOT HAND-EDIT THE DEPLOYED COPY to carry the missing text across. That makes\n" +
		"the file diverge from source with no expiry and no record: the next legitimate\n" +
		"update is either clobbered silently or declined into a .dist sidecar that\n" +
		"somebody discovers days later. It trades a known gap for an invisible one.\n\n" +
		"UNTIL IT IS INSTALLED, treat the affected file as a prompt you cannot fully\n" +
		"trust rather than as a prompt that is fine. The costly shape of this defect is\n" +
		"not a missing paragraph, it is a live prompt that ASSERTS something no longer\n" +
		"true — and the agent reading it has no way to know its copy is superseded. That\n" +
		"is what this notice is for.\n\n")

	fmt.Fprintf(&b, "SCOPE OF THE SWEEP. %d shipped prompt(s) were compared against %s; %d differ or\n"+
		"are missing. %d installed file(s) the ref does not ship were NOT judged — the\n"+
		"deployed file is the source for those and staleness is not a question about\n"+
		"them.\n", r.Shipped, r.Root, len(r.Findings), len(r.Unjudged))

	return b.String()
}

func shortSHA(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// referenceAgeLine dates the reference's last fetch, or says why it could not
// be dated. An unknown age is printed as unknown: a zero would read as "just
// fetched", which is the reading that makes a frozen mirror look current.
func referenceAgeLine(r Report) string {
	f := r.Reference.Fetch
	if !f.Known() {
		why := f.Why
		if why == "" {
			why = "no fetch record"
		}
		return fmt.Sprintf("    fetched:   UNKNOWN — %s\n", why)
	}
	return fmt.Sprintf("    fetched:   %s (%s ago) — the ref can only have seen what shipped before then\n",
		f.At, time.Duration(f.AgeSeconds)*time.Second)
}

// remoteLine states whether the reference itself is behind the live remote, and
// says what that does to the verdict above.
//
// It is a qualifier and never a finding. Origin moves all day; a notice on every
// push would be filtered inside a week, and "the nightly did not run" is already
// owned by the deploy witness.
func remoteLine(r Report) string {
	st := r.Remote
	switch {
	case r.RemoteSkipped:
		return "    remote:    NOT QUERIED — this sweep is configured not to consult the live\n" +
			"               remote, so the verdict is 'matches the reference' and whether the\n" +
			"               reference has itself seen what shipped is unknown here. `pogo\n" +
			"               check-staleness` answers it.\n"
	case !st.Armed:
		return "    remote:    NOT COMPARED — no remote head to check this reference against, so\n" +
			"               the verdict is 'matches the reference' with no qualifier available\n"
	case st.Err != "":
		return fmt.Sprintf("    remote:    COULD NOT BE CONSULTED (%s) — the comparison above is\n"+
			"               unaffected; what is missing is the qualifier\n", st.Err)
	case !st.Behind:
		return "    remote:    up to date with the live remote head — the verdict above is about\n" +
			"               what SHIPPED, not merely what was deployed\n"
	case st.Counted:
		return fmt.Sprintf("    remote:    BEHIND by %d commit(s), %d of which touch the prompt corpus.\n"+
			"               The findings above may therefore UNDERSTATE the drift.\n",
			st.Commits, st.CorpusCommits)
	default:
		return "    remote:    BEHIND the live head, and by how much CANNOT be determined from\n" +
			"               here — this reference has not fetched those objects. The verdict\n" +
			"               above is 'the fleet matches what was DEPLOYED', not what shipped;\n" +
			"               `pogo check-staleness --fetch` answers the stronger question.\n"
	}
}

// selfCeilingLines states what the daemon that took this reading CARRIES, and
// it is the difference between a notice a recipient can act on and one that
// sends them at a command that cannot work (mg-1e8e).
//
// A prompt corpus is capped at the revision of the process that installs it.
// pogod calls agent.InstallPrompts at every boot, so when its own embed already
// matches what is installed, that boot install is a NO-OP — it will keep
// reporting `changed=0 ... ok=true` for as long as the daemon runs, which on
// this box was seven consecutive sweeps over a mayor.md 129 lines behind. The
// recipient reading "your prompt is stale, run `pogo agent prompt install`"
// would have run it against the very daemon that had just declined to change
// anything, and concluded the tool was broken.
//
// Only the findings addressed to THIS recipient are judged. A ceiling over
// files somebody else was mailed about is not a fact this reader can use, and
// the counts would not match the list printed above it.
func selfCeilingLines(rc Recipient, r Report) string {
	c := r.SelfCeiling
	if c == nil {
		return ""
	}
	mine := map[string]bool{}
	for _, f := range rc.Findings {
		mine[f.Path] = true
	}
	var frozen, closes, third []string
	for _, p := range c.Closes {
		if mine[p] {
			closes = append(closes, p)
		}
	}
	for _, p := range c.Frozen {
		if mine[p] {
			frozen = append(frozen, p)
		}
	}
	for _, p := range c.Third {
		if mine[p] {
			third = append(third, p)
		}
	}

	var b strings.Builder
	b.WriteString("\nWHAT THE DAEMON THAT MAILED YOU CARRIES. pogod installs prompts from its OWN\n" +
		"embedded copy, at every boot. That caps what any automatic install can produce,\n" +
		"so it decides whether an install is your remedy or a no-op:\n\n")

	if !c.Known() {
		fmt.Fprintf(&b, "    UNKNOWN — %s\n"+
			"    That is not an all-clear. Nothing was established about the automatic path.\n", c.Unknown)
		return b.String()
	}

	switch {
	case len(frozen) > 0 && len(closes) == 0 && len(third) == 0:
		fmt.Fprintf(&b, "    This pogod carries EXACTLY what is already installed, for all %d file(s)\n"+
			"    above. Its boot install is a NO-OP and has been reporting ok=true doing it.\n"+
			"    An install from this daemon cannot close this gap; a NEWER BINARY has to\n"+
			"    land AND BE RUN first. If the nightly is building but not restarting, that\n"+
			"    is the failure to chase, not this one.\n", len(frozen))
	case len(closes) > 0 && len(frozen) == 0 && len(third) == 0:
		fmt.Fprintf(&b, "    This pogod carries the SHIPPED content for all %d file(s) above, so a\n"+
			"    restart of this daemon — or `pogo agent prompt install` from a build of it —\n"+
			"    would install them. That it has not means the install was declined rather\n"+
			"    than impossible; check for a .dist sidecar (`pogo check-prompt-edits`).\n", len(closes))
	case len(third) > 0 && len(closes) == 0 && len(frozen) == 0:
		fmt.Fprintf(&b, "    This pogod carries a THIRD version of all %d file(s) above — neither what\n"+
			"    is installed nor what the reference ships. That is what a daemon AHEAD of\n"+
			"    the reference looks like, and the reference here is a lagging mirror. Run\n"+
			"    `pogo check-staleness --fetch` before acting: the gap may be the\n"+
			"    reference's, not yours.\n", len(third))
	default:
		fmt.Fprintf(&b, "    MIXED over the %d file(s) above: %d it carries as shipped, %d already\n"+
			"    installed (an install changes nothing there), %d a third version.\n",
			len(closes)+len(frozen)+len(third), len(closes), len(frozen), len(third))
		if len(frozen) > 0 {
			fmt.Fprintf(&b, "    Already installed here, so no install will move them: %s\n", strings.Join(frozen, ", "))
		}
	}
	b.WriteString("    An upper bound, not a forecast: what it does not carry it cannot write, and\n" +
		"    what it does carry it may still decline to write over a hand-edited copy.\n")
	return b.String()
}
