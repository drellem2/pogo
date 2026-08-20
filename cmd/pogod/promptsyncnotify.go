package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// A declined prompt sync is the first pogod condition to get an addressed
// channel rather than only a log line, and mg-c3f0 is the ticket that explains
// why that distinction matters.
//
// THE DEFECT BEING FIXED IS NOT THE MESSAGE. promptRefreshLogLines already
// reports a declined sync correctly: it names the file, names the .dist
// sidecar, states the remedy, and links the doc (mg-f86c). That line fired at
// every boot for at least seven days — 07-29 and 07-30 are both confirmed in
// ~/Library/Logs/pogo/pogod.log — and nobody acted on it, because pogod.log is
// on no agent's schedule. The mayor ran 13-day-stale guidance the whole time
// and it was found by accident, by the architect reading the log while writing
// an unrelated design (mg-3ebe item 5). An alarm with no reader is worse than
// silence: the system genuinely reports the problem and therefore AUDITS as
// instrumented.
//
// WHY THE AFFECTED AGENT AND NOT `human`. The `human` maildir has ~800 unread
// going back to April, so routing a fleet condition there would reproduce
// exactly the defect this fixes while looking like a fix. The agent whose
// prompt was declined is the one that can act: it can read both files, knows
// which of its local customizations are load-bearing, and is the party actually
// harmed by running stale guidance.
//
// WHY THIS IS NOT A LOG TAILER. pogod knows it declined the sync at the moment
// it declined it — InstallPrompts hands back the conflict set in-process.
// Tailing a log to recover information the emitter already had would be a
// strictly worse design, so this runs at the decision point, on the same boot,
// before crew auto-start. That ordering is a feature: the mail lands in the
// affected agent's maildir BEFORE that agent starts, so it hears on its very
// first mail-check of the boot that declined its prompt.
//
// WHY IT DOES NOT MAIL EVERY BOOT. See promptSyncNotifyState — seven identical
// mails is how a real alarm gets filtered out, so this notifies on TRANSITION
// INTO the condition and re-notifies only while it remains unresolved.

const (
	// promptSyncRenotifyAfter is how long an unreconciled conflict stays quiet
	// between reminders. Deliberately not daily: reconciling a prompt is a
	// judgement about which local edits are load-bearing, not a command to run,
	// and a nag that arrives faster than the work can reasonably be scheduled
	// trains the recipient to filter it — which is the failure mode of the
	// original incident, one level up. Three days puts about two reminders in
	// the seven-day window that went unnoticed, instead of seven identical
	// ones.
	promptSyncRenotifyAfter = 72 * time.Hour

	// promptSyncNoticesFile holds the transition state. It sits at the
	// POGO_HOME root beside the other small daemon state files
	// (coordinator.json, polecat-witness.json, refinery-state.json).
	promptSyncNoticesFile = "prompt-sync-notices.json"

	// promptSyncDeclinedEvent is the structured record of the condition. It is
	// emitted for EVERY conflict on EVERY boot, including the boots where the
	// mail is suppressed, which is what makes a notifier that has quietly
	// stopped distinguishable from a fleet that simply has no conflicts.
	promptSyncDeclinedEvent = "prompt_sync_declined"
)

// promptSyncNoticeState is one remembered notification.
type promptSyncNoticeState struct {
	// Fingerprint identifies WHICH divergence was announced, not merely that
	// one was. It is the content hash of the .dist sidecar, so a later pogo
	// version that ships a further change to the same prompt re-notifies: the
	// divergence has grown and the recipient's merge job is now bigger.
	Fingerprint string `json:"fingerprint"`
	// NotifiedAt is stamped only on a SUCCESSFUL send (see
	// notifyPromptSyncConflicts) — a mail that failed must not be remembered as
	// delivered, or the retry never happens and the alarm dies silently.
	NotifiedAt time.Time `json:"notified_at"`
	// To records where it went, so a misroute is auditable after the fact.
	To string `json:"to"`
}

// promptSyncNotices is the on-disk transition store.
//
// WHY THIS NEEDS TO BE ON DISK AT ALL. A declined sync is not a transient
// event, it is a steady state: InstallPrompts leaves the user-edited canonical
// untouched, so that file keeps its OLD embed stamp, so the next boot compares
// the same stale stamp against the same shipped embed and declines again. The
// conflict set is therefore re-derived identically at every boot — which is
// precisely why the log line fired seven times — and in-process memory cannot
// suppress it, because the process restart IS the tick.
type promptSyncNotices struct {
	Version   int                              `json:"version"`
	Conflicts map[string]promptSyncNoticeState `json:"conflicts"`
}

func promptSyncNoticesPath() string {
	return filepath.Join(config.PogoHome(), promptSyncNoticesFile)
}

// loadPromptSyncNotices reads the store, treating every failure as "nothing
// remembered".
//
// THE BIAS HERE IS DELIBERATE AND IT IS TOWARD NOISE. An unreadable or corrupt
// store makes this notifier forget, so it re-notifies — at worst a duplicate
// mail. The opposite bias, treating a read failure as "already told them",
// would let a corrupt file silently disable the alarm, which is the class of
// defect mg-c3f0 exists to remove. Never fail toward silence.
func loadPromptSyncNotices(path string) promptSyncNotices {
	empty := promptSyncNotices{Version: 1, Conflicts: map[string]promptSyncNoticeState{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var n promptSyncNotices
	if err := json.Unmarshal(data, &n); err != nil {
		log.Printf("pogod: prompt-sync notice store %s is unreadable (%v) — re-announcing any conflicts", path, err)
		return empty
	}
	if n.Conflicts == nil {
		n.Conflicts = map[string]promptSyncNoticeState{}
	}
	n.Version = 1
	return n
}

func savePromptSyncNotices(path string, n promptSyncNotices) error {
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// promptSyncAddressee resolves the relative path of a declined prompt to the
// agent that can act on it, and reports whether that agent OWNS the prompt or
// is merely the fallback.
//
// The routing table itself lives in internal/agent beside ListPrompts, which is
// the authority on how a file under ~/.pogo/agents/ becomes an agent. It moved
// there when a second caller appeared — the hand-edit detector (mg-0c96), which
// must address exactly the same agents for exactly the same reason — because a
// second copy of a routing table is a second chance to misroute, and mail to a
// name no agent reads is silently accepted into a phantom mailbox and lost.
//
// This wrapper stays so the notifier reads at its own level of abstraction and
// so the tests that pin this behaviour keep pinning it from the caller's side.
func promptSyncAddressee(rel, coordinator string) (to string, owned bool) {
	return agent.PromptAddressee(rel, coordinator)
}

// promptSyncFingerprint hashes the shipped content that was declined.
//
// On a read failure it returns a STABLE placeholder rather than something that
// varies per boot. A fingerprint that changed every time would defeat the
// suppression and mail on every boot — the exact behaviour being fixed. The
// cost of the stable fallback is that a later change to a .dist we cannot read
// does not earn its own re-notification; it still gets the renotify-interval
// reminder.
func promptSyncFingerprint(distAbs string) string {
	data, err := os.ReadFile(distAbs)
	if err != nil {
		return "unread"
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// promptSyncMailer is the send seam. Production wires client.SendMGMail; tests
// substitute a recorder. Without this seam a test run shells out to the real
// `mg` and mails a live crew agent — a manufactured fleet alarm, which is the
// same class of fault as writing test events onto the real spine.
type promptSyncMailer func(to, from, subject, body string) error

// notifyPromptSyncConflicts is the whole notifier: for each declined prompt,
// decide whether this boot is a transition into (or a still-unresolved
// continuation of) the condition, and if so mail the agent that can act.
//
// Returns the number of mails actually sent, for the caller's log line and for
// tests.
func notifyPromptSyncConflicts(
	res *agent.InstallResult,
	coordinator string,
	promptDir string,
	statePath string,
	now time.Time,
	mail promptSyncMailer,
) int {
	if res == nil || len(res.Conflicts) == 0 {
		// No conflicts: forget everything, so that a conflict which comes back
		// later reads as a fresh transition and mails immediately instead of
		// inheriting a suppression window from a previous, resolved incident.
		if n := loadPromptSyncNotices(statePath); len(n.Conflicts) > 0 {
			n.Conflicts = map[string]promptSyncNoticeState{}
			if err := savePromptSyncNotices(statePath, n); err != nil {
				log.Printf("pogod: could not clear prompt-sync notice store %s: %v", statePath, err)
			}
		}
		return 0
	}

	notices := loadPromptSyncNotices(statePath)
	next := map[string]promptSyncNoticeState{}
	sent := 0

	for _, c := range res.Conflicts {
		to, owned := promptSyncAddressee(c.Path, coordinator)
		fp := promptSyncFingerprint(filepath.Join(promptDir, c.DistPath))

		prev, seen := notices.Conflicts[c.Path]
		reason := ""
		switch {
		case !seen:
			reason = "new" // transition INTO the condition
		case prev.Fingerprint != fp:
			reason = "changed" // the declined update is not the one we announced
		case now.Sub(prev.NotifiedAt) >= promptSyncRenotifyAfter:
			reason = "unresolved" // still not reconciled after the quiet window
		case prev.To != to:
			reason = "readdressed" // the coordinator was renamed under us
		default:
			// Suppressed. Carry the previous stamp forward so the renotify
			// clock keeps running from the last DELIVERY, not from this boot.
			next[c.Path] = prev
			emitPromptSyncDeclined(c, to, owned, fp, false, "suppressed", "")
			continue
		}

		subject, body := promptSyncMailText(c, to, owned)
		if err := mail(to, "pogod-promptsync", subject, body); err != nil {
			// The notifier failed. Say so loudly AND do not remember this
			// conflict as announced: dropping it from the store means the next
			// boot treats it as new and tries again. A notifier that silently
			// stops is the same defect one level up, so the failure is both
			// logged and on the event spine with the error attached.
			log.Printf("pogod: ⚠ prompt-sync notice to %s FAILED for %s (%v) — "+
				"the declined sync is unannounced; retrying on next boot", to, c.Path, err)
			emitPromptSyncDeclined(c, to, owned, fp, false, reason, err.Error())
			continue
		}

		next[c.Path] = promptSyncNoticeState{Fingerprint: fp, NotifiedAt: now, To: to}
		sent++
		log.Printf("pogod: prompt-sync notice mailed to %s for %s (%s)", to, c.Path, reason)
		emitPromptSyncDeclined(c, to, owned, fp, true, reason, "")
	}

	// Only the paths still in conflict survive, so a reconciled prompt is
	// forgotten and a recurrence mails immediately.
	notices.Conflicts = next
	if err := savePromptSyncNotices(statePath, notices); err != nil {
		// Failing to persist means the next boot re-announces. That is the
		// harmless direction; log it and carry on.
		log.Printf("pogod: could not persist prompt-sync notice store %s: %v — "+
			"conflicts may be re-announced next boot", statePath, err)
	}
	return sent
}

// emitPromptSyncDeclined puts the condition on the event spine every boot,
// whether or not a mail went out.
//
// This is the answer to "how is the notifier observable". The mail is the
// action; the event is the evidence. A reader of the spine can see the
// condition persisting, see which boots mailed and which suppressed, and see a
// send that failed — so a notifier that has quietly stopped working is
// visible as a run of `notified: false, reason: new` events, rather than
// looking identical to a fleet with no conflicts.
func emitPromptSyncDeclined(c agent.PromptConflict, to string, owned bool, fingerprint string, notified bool, reason, mailErr string) {
	details := map[string]any{
		"path":        c.Path,
		"dist_path":   c.DistPath,
		"addressee":   to,
		"owner":       owned,
		"fingerprint": fingerprint,
		"notified":    notified,
		"reason":      reason,
	}
	if mailErr != "" {
		details["mail_error"] = mailErr
	}
	events.Emit(context.Background(), events.Event{
		EventType: promptSyncDeclinedEvent,
		// Attributed to the affected agent, not to pogod: the condition is
		// about that agent's prompt, so `pogo events --agent <name>` should
		// show it in that agent's history.
		Agent:   to,
		Details: details,
	})
}

// promptSyncMailText writes the notice. It states the condition, names both
// files, and states the remedy as a DECISION rather than a paste-ready command
// — the whole reason the canonical file was preserved is that the local edits
// might be load-bearing, so handing out a copy-over one-liner would hand out
// the one action that destroys the thing this mechanism protected.
func promptSyncMailText(c agent.PromptConflict, to string, owned bool) (subject, body string) {
	root := agent.PromptDir()

	if owned {
		subject = fmt.Sprintf("[prompt-sync] YOUR prompt %s was not updated — reconcile it with %s",
			c.Path, c.DistPath)
	} else {
		subject = fmt.Sprintf("[prompt-sync] %s was not updated — reconcile it with %s",
			c.Path, c.DistPath)
	}

	whose := "A prompt file you are responsible for"
	if owned {
		whose = "YOUR OWN prompt file"
	}

	body = fmt.Sprintf(
		"%s was NOT updated when this pogod booted, and the shipped update is sitting unmerged beside it.\n\n"+
			"Canonical (in use, yours):  %s\n"+
			"Shipped update (declined):  %s\n\n"+
			"WHAT HAPPENED. A new pogo binary shipped a changed version of this prompt, and the\n"+
			"canonical file had local edits. pogod will not overwrite local edits, so it preserved\n"+
			"the file you are running and diverted the shipped update to the .dist sidecar. The\n"+
			"propagation that a pogod boot is supposed to guarantee did NOT happen for this file,\n"+
			"and it will not happen on any later boot either: nothing retries, because there is\n"+
			"nothing safe to retry. Until someone merges the two, you keep running the old text.\n\n"+
			"TO RESOLVE. Diff them and merge deliberately — this is a judgement, not a command:\n\n"+
			"    diff -u %s %s\n\n"+
			"Keep the local edits that are still load-bearing, take the shipped changes, and write\n"+
			"the result back to the canonical file. Then remove the .dist sidecar; its presence is\n"+
			"what marks this unreconciled. Do NOT simply copy .dist over the canonical file without\n"+
			"reading the diff — the local edits are the only reason the update was declined, and\n"+
			"copying over them is the one outcome this mechanism exists to prevent.\n"+
			"See docs/prompt-customization.md for the merge workflow.\n\n"+
			"WHY YOU ARE GETTING MAIL ABOUT IT. pogod has always logged this correctly, by name,\n"+
			"with the remedy. It logged it at every boot for seven days and nobody acted, because\n"+
			"pogod.log is on no agent's schedule — the mayor ran 13-day-stale guidance the whole\n"+
			"time and it was found by accident during unrelated work (mg-3ebe, mg-c3f0). An alarm\n"+
			"nobody reads audits as instrumented and is worse than silence, so this condition now\n"+
			"reaches the agent that can act on it. You will not get this every boot: it is sent when\n"+
			"the conflict first appears, when the declined update changes, and then at most once\n"+
			"every %s while it stays unreconciled.",
		whose,
		filepath.Join(root, c.Path),
		filepath.Join(root, c.DistPath),
		filepath.Join(root, c.Path),
		filepath.Join(root, c.DistPath),
		promptSyncRenotifyAfter)

	return subject, body
}
