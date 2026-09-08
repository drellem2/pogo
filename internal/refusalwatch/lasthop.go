package refusalwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// # THE LAST HOP
//
// mg-6f3d's probe ends one hop short. Its strongest arm stats the maildir file
// and says: the alarm's bytes are in the directory the notifier polls. That is
// delivery, and it is a real improvement over the sixteen `routed_to: nobody`
// emissions of 2026-09-07 — but the chain is
//
//	detector -> alarm -> maildir -> notifier -> a person
//
// and mg-6f3d's own polecat wrote down that it had not measured the fourth
// arrow: "Whether an alarm is DISTINGUISHABLE in that volume is a property of
// the notifier, not of this change, and I have not measured it."
//
// It was right to decline, and it was right that the question is real. On
// 2026-09-08 the box the alarm lands in, `~/.macguffin/mail/human/new`, held
// 3,286 unread files against 43 in cur/. A file arriving in a directory that
// size changes nothing observable unless something downstream picks it out.
//
// This is that measurement, run as a probe rather than asserted in prose. It
// drives the REAL notifier — `pogo-reminders`' poll-mail.sh, the script
// com.pogo.deadman executes — over a throwaway maildir seeded with the real
// alarm bytes, with notify.sh stubbed so nothing reaches a screen.
//
// # What it constructs, and the controls that make the greens mean something
//
//   - the alarm, aged past the deadman's own 900s gate, in a maildir that
//     already holds a backlog: exactly ONE notification, carrying the subject.
//   - CONTROL, the same alarm NOT yet aged: ZERO notifications AND not marked
//     seen — a gate that marks a too-young message seen is the deadman's
//     silence arriving by a different route (mg-65d2).
//   - the alarm arriving inside a BURST that the notifier's incident grouping
//     collapses: the burst becomes one group notification and the alarm keeps
//     its own.
//   - CONTROL, the same burst with the alarm's sender inside the episode
//     roster: it IS swallowed into the group. Without this arm the previous one
//     is green because grouping never fired, not because the alarm escaped it.
//
// # What this probe does NOT claim
//
// A notification was dispatched is not a person read it. This probe measures
// the notifier's decision — one interruption, its own banner, the subject in
// the title — and stops there, which is the boundary at which it can still be
// honest. Whether 3,286 unread is itself the reason nobody reads is a question
// about the backlog's cause, and the ticket that raised this one deliberately
// left it open.
//
// # Why the notifier's top rank is unreachable from here
//
// poll-mail.sh's build_plan dispatches lowest-salience first, so the LAST
// notification is the most visible one. Its top rank is granted only to a reply
// whose referenced request is verified to exist and to come from human/daniel.
// An alarm pogod raises unprompted references no such request, so it is dispatched
// in the middle rank, level with the watcher traffic that is 44% of that box. That
// is a property of the notifier, recorded here as Detail rather than asserted as a
// failure: the alarm is delivered and distinguishable, it is simply not privileged.

// NotifierScriptEnv overrides the notifier this probe drives. Set it to point at
// a checkout rather than the deployed copy.
const NotifierScriptEnv = "POGO_NOTIFIER_SCRIPT"

// DeadmanMinAgeSeconds is com.pogo.deadman's MIN_AGE_SECONDS: how long a message
// must sit unclaimed in `human/new` before the bypass delivers it. Kept here so
// the probe exercises the gate the live job runs with rather than a convenient
// one.
const DeadmanMinAgeSeconds = 900

// lastHopBacklog is how many already-seen routine mails sit in the probe's
// maildir. It is small on purpose: the scan cost is linear in the file count
// (measured 2026-09-08: 26.9ms per already-seen file per cycle, 88.4s for a
// 3,288-file directory), so a probe seeded with the live backlog would spend a
// minute and a half of the merge gate re-measuring a slope. What the backlog is
// here to test is that a non-empty directory does not swallow the alarm, and
// that needs it non-empty, not enormous.
const lastHopBacklog = 60

// NotifierScript resolves the notifier poll-mail.sh drives.
//
// It lives in another repo (pogo-reminders) and is deployed to ~/.pogo. There is
// no compile-time link, so an absent script is BLIND — the probe measured
// nothing — and never a pass.
func NotifierScript() (string, error) {
	if p := strings.TrimSpace(os.Getenv(NotifierScriptEnv)); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("%s=%s names nothing readable", NotifierScriptEnv, p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("no home directory in which to find the notifier")
	}
	for _, cand := range []string{
		filepath.Join(home, ".pogo", "pogo-reminders", "bin", "poll-mail.sh"),
		filepath.Join(home, "dev", "pogo-reminders", "bin", "poll-mail.sh"),
	} {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no poll-mail.sh under ~/.pogo/pogo-reminders/bin or ~/dev/pogo-reminders/bin; "+
		"set %s to point at one", NotifierScriptEnv)
}

// notifierLookup is NotifierScript, indirected so a test can drive the BLIND
// branch on purpose — same reason as mgLookup.
var notifierLookup = NotifierScript

// notification is one banner the notifier decided to raise.
type notification struct {
	Group string
	Title string
}

// ProbeLastHop drives the real notifier over the real alarm bytes.
//
// Like Probe, a setup failure sets Blind and is neither a pass nor a failure.
func ProbeLastHop() ProbeResult {
	res := ProbeResult{}

	// poll-mail.sh is macOS-only by construction: BSD `stat -f %m`, osascript,
	// launchd. Saying so is the honest blind, not a skip that reads as green.
	if runtime.GOOS != "darwin" {
		res.Blind = "the notifier is macOS-only (BSD stat, osascript, launchd); this box is " + runtime.GOOS
		return res
	}
	bin, err := mgLookup()
	if err != nil {
		res.Blind = err.Error() + "; the last-hop probe delivers the alarm through the real mg before the notifier sees it"
		return res
	}
	res.MG = bin
	script, err := notifierLookup()
	if err != nil {
		res.Blind = err.Error() + "; without the notifier there is no last hop to measure"
		return res
	}

	dir, err := os.MkdirTemp("", "refusalwatch-lasthop-")
	if err != nil {
		res.Blind = fmt.Sprintf("could not create a throwaway store: %v", err)
		return res
	}
	defer os.RemoveAll(dir)

	binDir, err := stageNotifier(dir, script)
	if err != nil {
		res.Blind = err.Error()
		return res
	}

	now := time.Now().UTC()
	alarm := probeAlarm(now)

	// ---------------- ARM A: the last hop, in the condition it must survive.
	//
	// The alarm's real bytes, delivered by the same sink pogod uses, sitting in a
	// maildir that already holds routine traffic — and aged past the deadman's
	// own 900s gate, because that is the gate the live job applies.
	runA, err := newLastHopRun(dir, "aged", bin, binDir)
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	res.Store = runA.root
	alarmPath, err := runA.deliver(alarm, "pogod")
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := runA.seedBacklog(lastHopBacklog, now); err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := backdate(alarmPath, now.Add(-time.Hour)); err != nil {
		res.Blind = err.Error()
		return res
	}
	notesA, err := runA.poll()
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	res.Arms = append(res.Arms, ProbeArm{
		Name: fmt.Sprintf("the alarm lands in a maildir already holding %d messages and the REAL notifier raises it", lastHopBacklog),
		Want: "exactly 1 notification, titled with the alarm's subject",
		Got:  describeNotifications(notesA, "FLEET STOPPED"),
		OK:   len(notesA) == 1 && strings.Contains(notesA[0].Title, "FLEET STOPPED"),
		Detail: "mg-6f3d confirmed the bytes reach the directory; this is the hop after that. " +
			"A backlog does not suppress the alarm because the notifier keys on its own seen-set, " +
			"not on how full the directory is.",
	})

	// ---------------- ARM B: the deadman's gate, as a matched control.
	//
	// The same alarm, too YOUNG. It must not be notified — and it must not be
	// marked seen either, or it is silently dropped instead of held (mg-65d2).
	runB, err := newLastHopRun(dir, "young", bin, binDir)
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	youngPath, err := runB.deliver(alarm, "pogod")
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := runB.seedBacklog(lastHopBacklog, now); err != nil {
		res.Blind = err.Error()
		return res
	}
	notesB, err := runB.poll()
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	heldB, seenErr := runB.isSeen(filepath.Base(youngPath))
	gotB := describeNotifications(notesB, "FLEET STOPPED")
	if seenErr != nil {
		gotB += fmt.Sprintf(" (seen-set unreadable: %v)", seenErr)
	} else if heldB {
		gotB += " and MARKED SEEN"
	} else {
		gotB += " and held unseen for a later cycle"
	}
	res.Arms = append(res.Arms, ProbeArm{
		Name: fmt.Sprintf("CONTROL: an alarm younger than the deadman's %ds gate raises nothing YET, and is not consumed", DeadmanMinAgeSeconds),
		Want: "0 notifications, and the message still unseen",
		Got:  gotB,
		OK:   len(notesB) == 0 && seenErr == nil && !heldB,
		Detail: "if this arm goes green by marking the message seen, the alarm is dropped rather than " +
			"held, and arm A is green only for alarms that happen to be old enough on their first cycle",
	})

	// ---------------- ARM C: distinguishable INSIDE a burst.
	//
	// The notifier collapses a burst of crew mail that shares an incident episode
	// into one banner (mg-82d3/mg-e0f6). That is the mechanism most likely to
	// swallow an alarm arriving in the same cycle. Construct the burst, with a
	// real episode record covering it, and require the alarm to keep its own.
	runC, err := newLastHopRun(dir, "burst", bin, binDir)
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	burstSenders := []string{"ack-watch", "stall-watch", "turn-watch", "fleet-liveness-probe"}
	burstPaths, err := runC.seedBurst(burstSenders, 3, now)
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	alarmC, err := runC.deliver(alarm, "pogod")
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := runC.writeEpisode("ep-lasthop", burstSenders, now); err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := backdateAll(append(burstPaths, alarmC), now.Add(-time.Hour)); err != nil {
		res.Blind = err.Error()
		return res
	}
	notesC, err := runC.poll()
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	ownC := ownBanner(notesC, "FLEET STOPPED")
	groupedC := countGroups(notesC)
	res.Arms = append(res.Arms, ProbeArm{
		Name: fmt.Sprintf("the alarm arrives in the same cycle as a %d-message watcher burst the notifier COALESCES", len(burstPaths)),
		Want: "the burst collapses to 1 group banner and the alarm keeps its OWN",
		Got: fmt.Sprintf("%d banner(s), %d of them a coalesced group; the alarm's own banner is %s",
			len(notesC), groupedC, presence(ownC)),
		OK: ownC && groupedC == 1 && len(notesC) == 2,
		Detail: "poll-mail.sh dispatches lowest salience first, so the LAST banner is the most visible. " +
			"Its top rank is reachable only by a reply to a request verified to come from human/daniel, " +
			"which an unprompted alarm never is — so the alarm is distinguishable but not privileged. " +
			"That is the notifier's design, recorded, not a failure of this delivery.",
	})

	// ---------------- ARM D: the control that makes ARM C mean something.
	//
	// The same burst, the same episode record, but the alarm sent FROM a roster
	// member. Grouping must swallow it. If it does not, ARM C was green because
	// coalescing never fired at all, and this probe would be measuring nothing.
	runD, err := newLastHopRun(dir, "swallowed", bin, binDir)
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	burstD, err := runD.seedBurst(burstSenders, 3, now)
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	alarmD, err := runD.deliver(alarm, burstSenders[0])
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := runD.writeEpisode("ep-lasthop", burstSenders, now); err != nil {
		res.Blind = err.Error()
		return res
	}
	if err := backdateAll(append(burstD, alarmD), now.Add(-time.Hour)); err != nil {
		res.Blind = err.Error()
		return res
	}
	notesD, err := runD.poll()
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	ownD := ownBanner(notesD, "FLEET STOPPED")
	res.Arms = append(res.Arms, ProbeArm{
		Name: "CONTROL: the SAME alarm sent from inside the burst's roster IS swallowed by the grouping",
		Want: "no banner of its own — coalescing demonstrably fires",
		Got:  fmt.Sprintf("%d banner(s); the alarm's own banner is %s", len(notesD), presence(ownD)),
		OK:   !ownD && len(notesD) == 1,
		Detail: "without this arm, arm C is green whenever grouping is dead — the specific way an " +
			"instrument goes green because it cannot see rather than because there is nothing to see. " +
			"It also names the real hazard: the alarm escapes grouping because pogod is in no episode " +
			"roster, and nothing enforces that.",
	})

	return res
}

// lastHopRun is one throwaway store plus the environment the notifier runs in.
type lastHopRun struct {
	root      string // <dir>/<name>/macguffin
	maildir   string // <root>/mail/human/new
	stateDir  string
	notifyLog string
	events    string
	home      string
	bin       string // mg
	binDir    string // staged notifier + stub notify.sh
}

func newLastHopRun(dir, name, bin, binDir string) (*lastHopRun, error) {
	base := filepath.Join(dir, name)
	r := &lastHopRun{
		root:      filepath.Join(base, "macguffin"),
		stateDir:  filepath.Join(base, "state"),
		notifyLog: filepath.Join(base, "notifications.tsv"),
		events:    filepath.Join(base, "events.log"),
		home:      filepath.Join(base, "home"),
		bin:       bin,
		binDir:    binDir,
	}
	r.maildir = filepath.Join(r.root, "mail", "human", "new")
	for _, d := range []string{r.root, r.stateDir, r.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("could not build the throwaway store: %w", err)
		}
	}
	if out, err := runMG(bin, r.root, "init"); err != nil {
		return nil, fmt.Errorf("`mg init` on the throwaway store: %w\n%s", err, out)
	}
	if out, err := runMG(bin, r.root, "mail", "register", "human"); err != nil {
		return nil, fmt.Errorf("could not register the `human` mailbox: %w\n%s", err, out)
	}
	return r, nil
}

// deliver sends the alarm through the real MailSink and returns the maildir file.
func (r *lastHopRun) deliver(a Alarm, from string) (string, error) {
	rec, err := MailSink{Root: r.root, Bin: r.bin, To: "human", From: from}.Deliver(a)
	if err != nil || !rec.Confirmed {
		return "", fmt.Errorf("the alarm did not reach the throwaway maildir, so there is no last hop to measure: %s", rec.Err)
	}
	return rec.Ref, nil
}

// seedBacklog fills the maildir with already-seen routine mail and records it in
// the notifier's seen-set, which is the live condition: the box holds thousands
// of files the notifier has already raised once and will not raise again.
func (r *lastHopRun) seedBacklog(n int, now time.Time) error {
	seen := map[string]string{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%d.backlog.%d", now.Add(-24*time.Hour).UnixNano(), i)
		body := fmt.Sprintf("Message-Id: %s\nFrom: ack-watch\nSubject: ack-watch: 1 item unacknowledged, oldest 1h — mg-%04d\nDate: %s\n\nroutine\n",
			id, i, now.Add(-24*time.Hour).Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(r.maildir, id), []byte(body), 0o644); err != nil {
			return fmt.Errorf("could not seed the backlog: %w", err)
		}
		seen[id] = now.Add(-24 * time.Hour).Format("2006-01-02T15:04:05Z")
	}
	blob, err := json.Marshal(seen)
	if err != nil {
		return fmt.Errorf("could not seed the backlog: %w", err)
	}
	if err := os.WriteFile(filepath.Join(r.stateDir, "notify-seen.json"), blob, 0o644); err != nil {
		return fmt.Errorf("could not seed the notifier's seen-set: %w", err)
	}
	return nil
}

// seedBurst writes per-sender UNSEEN mail dated inside the episode window.
func (r *lastHopRun) seedBurst(senders []string, each int, now time.Time) ([]string, error) {
	var paths []string
	for si, sender := range senders {
		for i := 0; i < each; i++ {
			id := fmt.Sprintf("%d.burst.%d", now.Add(-30*time.Minute).UnixNano()+int64(si*100+i), si*100+i)
			body := fmt.Sprintf("Message-Id: %s\nFrom: %s\nSubject: %s: routine notice %d\nDate: %s\n\nroutine\n",
				id, sender, sender, i, now.Add(-30*time.Minute).Format(time.RFC3339))
			p := filepath.Join(r.maildir, id)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				return nil, fmt.Errorf("could not seed the burst: %w", err)
			}
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// writeEpisode writes one incident_episode_cleared boundary record of exactly the
// shape the notifier's load_episodes() accepts. The event type is read from the
// same constant the emitter uses via docs; a drift there makes grouping silently
// dead, which is what ARM D would catch.
func (r *lastHopRun) writeEpisode(id string, roster []string, now time.Time) error {
	rec := map[string]any{
		"event_type": "incident_episode_cleared",
		"agent":      "pogod",
		"timestamp":  now.Format(time.RFC3339Nano),
		"details": map[string]any{
			"kind":       "auth",
			"episode_id": id,
			"roster":     roster,
			"opened_at":  now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
			"closed_at":  now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
		},
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("could not write the episode record: %w", err)
	}
	return os.WriteFile(r.events, append(blob, '\n'), 0o644)
}

// isSeen reports whether the notifier consumed a message id.
func (r *lastHopRun) isSeen(id string) (bool, error) {
	blob, err := os.ReadFile(filepath.Join(r.stateDir, "notify-seen.json"))
	if err != nil {
		return false, err
	}
	var seen map[string]string
	if err := json.Unmarshal(blob, &seen); err != nil {
		return false, err
	}
	_, ok := seen[id]
	return ok, nil
}

// poll runs ONE notifier cycle and returns the banners it raised.
func (r *lastHopRun) poll() ([]notification, error) {
	_ = os.WriteFile(r.notifyLog, nil, 0o644)
	cmd := exec.Command("/bin/bash", filepath.Join(r.binDir, "poll-mail.sh"))
	cmd.Env = append(os.Environ(),
		"HOME="+r.home,
		"ONESHOT=true",
		"COALESCE=true",
		fmt.Sprintf("MIN_AGE_SECONDS=%d", DeadmanMinAgeSeconds),
		"SILENCE_INTERVAL=0",
		"MIRROR_REMINDER=false",
		"MAIL_DIR="+r.maildir,
		"MAIL_ROOT="+filepath.Join(r.root, "mail"),
		"STATE_DIR="+r.stateDir,
		"EVENTS_LOG="+r.events,
		"TITLE_PREFIX=[UNPROCESSED] ",
		"NOTIFY_LOG="+r.notifyLog,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("the notifier exited non-zero, so nothing it did can be read as a verdict: %w\n%s", err, out)
	}
	return readNotifications(r.notifyLog)
}

// stageNotifier copies the real notifier beside a stub notify.sh.
//
// A copy rather than the original because poll-mail.sh resolves notify.sh from
// its OWN directory, and the only way to keep this probe off Daniel's screen is
// to give it a different neighbour. The bytes are the deployed script's.
func stageNotifier(dir, script string) (string, error) {
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("could not stage the notifier: %w", err)
	}
	src := filepath.Dir(script)
	if err := copyFile(script, filepath.Join(binDir, "poll-mail.sh")); err != nil {
		return "", fmt.Errorf("could not stage the notifier: %w", err)
	}
	// heartbeat.sh is sourced defensively by poll-mail.sh, so a missing one is
	// survivable — copy it when it is there and do not fail when it is not.
	_ = copyFile(filepath.Join(src, "heartbeat.sh"), filepath.Join(binDir, "heartbeat.sh"))

	stub := "#!/bin/sh\n" +
		"# Stub notify.sh: records what the notifier decided, reaches no screen.\n" +
		"title=\"\"\ngroup=\"\"\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  case \"$1\" in\n    --title) title=\"$2\" ;;\n    --group) group=\"$2\" ;;\n  esac\n" +
		"  shift 2\ndone\n" +
		"printf '%s\\t%s\\n' \"$group\" \"$title\" >> \"$NOTIFY_LOG\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "notify.sh"), []byte(stub), 0o755); err != nil {
		return "", fmt.Errorf("could not stage the notifier stub: %w", err)
	}
	return binDir, nil
}

func copyFile(src, dst string) error {
	blob, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, blob, 0o755)
}

func readNotifications(path string) ([]notification, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the notifier left no record of what it raised: %w", err)
	}
	var out []notification
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		group, title, _ := strings.Cut(line, "\t")
		out = append(out, notification{Group: group, Title: title})
	}
	return out, nil
}

// backdate ages a maildir file past the deadman's gate. The gate reads mtime, not
// the filename's timestamp, so this is the only lever that moves it.
func backdate(path string, to time.Time) error {
	if err := os.Chtimes(path, to, to); err != nil {
		return fmt.Errorf("could not age %s past the notifier's gate: %w", filepath.Base(path), err)
	}
	return nil
}

func backdateAll(paths []string, to time.Time) error {
	for _, p := range paths {
		if err := backdate(p, to); err != nil {
			return err
		}
	}
	return nil
}

// ownBanner reports whether some banner carries the marker in its own title —
// i.e. the alarm was raised on its own terms rather than summarised inside a
// group banner, whose title never quotes a member's subject.
func ownBanner(notes []notification, marker string) bool {
	for _, n := range notes {
		if strings.Contains(n.Title, marker) {
			return true
		}
	}
	return false
}

// countGroups counts coalesced banners, which poll-mail.sh tags pogo-incident-*.
func countGroups(notes []notification) int {
	n := 0
	for _, note := range notes {
		if strings.HasPrefix(note.Group, "pogo-incident-") {
			n++
		}
	}
	return n
}

func describeNotifications(notes []notification, marker string) string {
	if len(notes) == 0 {
		return "0 notifications"
	}
	var titles []string
	for _, n := range notes {
		titles = append(titles, n.Title)
	}
	return fmt.Sprintf("%d notification(s), alarm subject %s: %s",
		len(notes), presence(ownBanner(notes, marker)), strings.Join(titles, " | "))
}
