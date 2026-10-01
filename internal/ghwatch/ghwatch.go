// Package ghwatch runs the three gh-issue watchers — intake (internal/ghintake),
// teardown (internal/ghteardown) and the carrier re-read (internal/carrierdrift)
// — as one standalone, scheduled command: `pogo gh-watch` (mg-257a8).
//
// # Why they left pogod
//
// Every one of them calls `gh`, and `gh` needs a GitHub credential. launchd
// execs pogod without a shell, so pogod has none of its own; internal/ghtoken
// fetched one through a zsh subshell at startup and injected it into the
// daemon's environment, where every child inherited it. That put a secret in a
// long-lived daemon mostly to serve these watchers, froze it at exec (a token
// rotated in ~/.zshenv stayed invisible to the intake scan for 173 hours,
// mg-4d59), and when the fetch failed every check read "unknown" (gh#113 was
// the second independent report of that confusion).
//
// Here the watchers run in a process launchd starts through a LOGIN SHELL
// (`zsh -c -l`, see internal/service's com.pogo.ghwatch), so the credential is
// the one the user's shell has, read afresh on every fire. Nothing long-lived
// holds it.
//
// pogod's own remaining `gh` calls (the refinery's PR-body closing-keyword guard
// and external-PR close, and strandedwork's awaiting-review probe) are
// merge-path, not scheduled, and are NOT moved by this package. They get a
// credential per call through ghtoken.ChildEnv, and pogod holds none of its own
// (mg-37183).
//
// # State between runs
//
// The watchers were written for a long-lived process: each keeps the time of
// its last sample (its coarse-interval throttle), the fingerprint of the last
// set it mailed (renotify), and a first-seen clock per finding (escalation).
// A per-fire process would lose all of it and mail every finding set on every
// fire. So Run takes the previous run's File and returns the next one, and the
// caller persists it (Store). The mg-scan caches (drellem2/pogo#179) are
// persisted the same way, or every fire would fork `mg show` once per item.
//
// # Results where pogod can read them
//
// The File doubles as the run's result: per detector, whether it armed and why
// not, whether it sampled this run, the last sample's event details, and the
// per-issue states the last sample actually read (so "closed" can be told apart
// from "checked nothing"). pogod reads it (cmd/pogod's gh-watch witness) to
// annunciate a detector that did not arm, and a job that stopped running at
// all — the one failure this process cannot report about itself.
package ghwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/carrierdrift"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/ghintake"
	"github.com/drellem2/pogo/internal/ghteardown"
	"github.com/drellem2/pogo/internal/watchstate"
)

const (
	// DirName is the state directory under POGO_HOME.
	DirName = "gh-watch"
	// StateFileName holds the File: watcher memory plus the last results.
	StateFileName = "state.json"
	// CacheFileName holds the persisted mg-scan caches.
	CacheFileName = "cache.json"
	lockFileName  = "lock"

	// Agent is the `agent` field on every event the watchers emit from here.
	Agent = "gh-watch"

	// SchemaVersion is File.SchemaVersion. Bump it on an incompatible change;
	// Read refuses a file whose version it does not know rather than guessing.
	SchemaVersion = 1
)

// Dir is the state directory for the POGO_HOME home.
func Dir(home string) string { return filepath.Join(home, DirName) }

// StatePath is the File's path for the POGO_HOME home.
func StatePath(home string) string { return filepath.Join(Dir(home), StateFileName) }

// Arming is a detector's arming outcome for one run.
type Arming string

const (
	// Armed: the preconditions held and the watcher ran (subject to its throttle).
	Armed Arming = "armed"
	// Disabled: turned off in config. Not a fault.
	Disabled Arming = "disabled"
	// NoGHBinary: `gh` is not on the job's PATH. Remedy: the job's PATH (it runs
	// through a login shell, so the shell profile's PATH).
	NoGHBinary Arming = "no_gh_binary"
	// NoCredential: gh is here and no GitHub credential could be established.
	// Remedy: `gh auth login`, or an export a login zsh reads.
	NoCredential Arming = "no_credential"
)

// DecideArming picks a detector's arming outcome.
//
// A missing binary is reported ahead of a missing credential: without `gh`,
// `gh auth token` cannot run either, so the credential predicate is false as a
// CONSEQUENCE of the same fault, and telling the operator to run `gh auth login`
// would hand them a command they do not have (mg-fb29, moved here from pogod's
// intakearming.go).
//
// needsCredential is false for teardown, which pogod armed on the binary alone;
// that is kept as it was, since its lookups classify an auth failure per carrier
// and it has never been the credential's reporter.
func DecideArming(enabled, ghOnPath, needsCredential, credentialOK bool) Arming {
	switch {
	case !enabled:
		return Disabled
	case !ghOnPath:
		return NoGHBinary
	case needsCredential && !credentialOK:
		return NoCredential
	default:
		return Armed
	}
}

// Lookup is one issue state a sample actually read.
type Lookup struct {
	Carrier string `json:"carrier"`
	Issue   string `json:"issue"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
}

// Detector is one watcher's record in the File.
type Detector struct {
	// Arming is this run's outcome; ArmingDetail says why when it is not Armed.
	Arming       Arming `json:"arming"`
	ArmingDetail string `json:"arming_detail,omitempty"`
	// SampledThisRun is false when the watcher's own interval throttle held it
	// (or it did not arm). Everything below it then describes an EARLIER run.
	SampledThisRun bool `json:"sampled_this_run"`
	// LastSampledAt is when the last sample ran.
	LastSampledAt time.Time `json:"last_sampled_at,omitempty"`
	// LastEvent and LastDetails are the event the last sample emitted and its
	// details. Teardown emits nothing on a clean sample; LastEvent then reads
	// "clean (no event)" so an empty record cannot be mistaken for one.
	LastEvent   string         `json:"last_event,omitempty"`
	LastDetails map[string]any `json:"last_details,omitempty"`
	// Lookups are the per-issue states the last sample read (teardown and the
	// carrier re-read; intake lists repos, not issues). The positive control:
	// a carrier whose issue is closed shows "closed" here, so "no findings" can
	// be told apart from "looked at nothing".
	Lookups []Lookup `json:"lookups,omitempty"`
	// Watcher is the watcher's persisted memory.
	Watcher watchstate.State `json:"watcher"`
}

// File is the persisted state and results of the most recent run.
type File struct {
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	PID           int       `json:"pid"`
	// Credential is ghtoken's existence-only description of the credential this
	// run found. Never the value.
	Credential   string   `json:"credential"`
	CredentialOK bool     `json:"credential_ok"`
	Teardown     Detector `json:"teardown"`
	Intake       Detector `json:"intake"`
	CarrierDrift Detector `json:"carrier_drift"`
	// IntakeWatch is the intake watch list this run resolved, recorded every
	// run whether or not intake sampled, so pogod can annunciate an armed
	// detector that is watching nothing (drellem2/pogo#121). Nil in a record
	// written before it existed — a reader must not take nil as "empty".
	IntakeWatch *WatchList `json:"intake_watch,omitempty"`
}

// WatchList is the intake detector's resolved watch list and where it came from
// (ghintake.ResolveRepos's two results).
type WatchList struct {
	Repos  []string `json:"repos"`
	Source string   `json:"source"`
}

// Detectors returns the three records by name, in a stable order.
func (f File) Detectors() []NamedDetector {
	return []NamedDetector{
		{"teardown", f.Teardown},
		{"intake", f.Intake},
		{"carrier_drift", f.CarrierDrift},
	}
}

// NamedDetector pairs a Detector with its name.
type NamedDetector struct {
	Name string
	Detector
}

// Deps are the run's side effects and I/O, injected so the run is testable.
type Deps struct {
	// GHPathErr is exec.LookPath("gh")'s error; nil means gh is on PATH.
	GHPathErr error
	// CredentialOK / CredentialSource / Credential describe the credential
	// (ghtoken.Result's OK, Source and String).
	CredentialOK     bool
	CredentialSource string
	Credential       string

	Mail func(to, from, subject, body string) error
	// Emit writes an event. Nil means events.Emit.
	Emit func(events.Event)

	TeardownSource ghteardown.SourceFunc
	TeardownLookup ghteardown.LookupFunc
	// IntakeSource returns the inventory to reconcile. It is handed the
	// credential state so production can bind it into ghintake.Collect.
	IntakeSource func() (ghintake.Inventory, error)
	// IntakeRepos / IntakeRepoSource are the watch list IntakeSource scans and
	// where it came from, recorded in File.IntakeWatch.
	IntakeRepos      []string
	IntakeRepoSource string
	DriftSource      carrierdrift.SourceFunc
	DriftSnapshot    carrierdrift.SnapshotFunc
	DriftStatuses    []string
}

// Run runs each enabled, armed watcher once — subject to its own interval
// throttle, restored from prev, unless force — and returns the File to persist.
//
// force samples every armed watcher now. It clears only the throttle: the
// renotify fingerprint and escalation clocks are kept, so a forced run does not
// re-mail an unchanged finding set or reset an escalation.
func Run(cfg *config.Config, prev File, deps Deps, now time.Time, force bool) File {
	emit := deps.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	escalateTo := cfg.Agents.EscalationBoxName()
	ghOnPath := deps.GHPathErr == nil
	ghDetail := ""
	if deps.GHPathErr != nil {
		ghDetail = deps.GHPathErr.Error()
	}

	out := File{
		SchemaVersion: SchemaVersion,
		StartedAt:     now,
		PID:           os.Getpid(),
		Credential:    deps.Credential,
		CredentialOK:  deps.CredentialOK,
		IntakeWatch: &WatchList{
			Repos:  append([]string{}, deps.IntakeRepos...),
			Source: deps.IntakeRepoSource,
		},
	}

	// arm fills d's arming fields and, when not armed, carries the previous
	// record forward so the last real sample is not lost to a bad fire.
	arm := func(p Detector, a Arming, credDetail string) Detector {
		d := p
		d.SampledThisRun = false
		d.Arming = a
		d.ArmingDetail = ""
		switch a {
		case NoGHBinary:
			d.ArmingDetail = ghDetail
		case NoCredential:
			d.ArmingDetail = credDetail
		}
		return d
	}

	// Teardown.
	out.Teardown = arm(prev.Teardown,
		DecideArming(cfg.GHTeardown.Enabled, ghOnPath, false, deps.CredentialOK), deps.Credential)
	if out.Teardown.Arming == Armed {
		rec := &recorder{}
		lookup, source := deps.TeardownLookup, deps.TeardownSource
		w := ghteardown.New(ghteardown.Options{
			Enabled: true,
			Agent:   Agent,
			Source: func() ([]ghteardown.Carrier, error) {
				cs, err := source()
				for _, c := range cs {
					rec.carrier(fmt.Sprintf("%s#%d", c.Repo, c.Number), c.ID)
				}
				return cs, err
			},
			Lookup: func(repo string, number int) (ghteardown.IssueState, error) {
				st, err := lookup(repo, number)
				rec.lookup(fmt.Sprintf("%s#%d", repo, number), string(st), err)
				return st, err
			},
			Mail:          deps.Mail,
			Emit:          rec.emitter(emit),
			Interval:      cfg.GHTeardown.Interval,
			RenotifyAfter: cfg.GHTeardown.RenotifyAfter,
			NotifyTo:      cfg.GHTeardown.NotifyTo,
			EscalateAfter: cfg.GHTeardown.EscalateAfter,
			EscalateTo:    escalateTo,
		})
		out.Teardown = sample(out.Teardown, w, rec, now, force)
	}

	// Intake.
	out.Intake = arm(prev.Intake,
		DecideArming(cfg.GHIntake.Enabled, ghOnPath, true, deps.CredentialOK), deps.Credential)
	if out.Intake.Arming == Armed {
		rec := &recorder{}
		w := ghintake.New(ghintake.Options{
			Enabled:       true,
			Agent:         Agent,
			Source:        deps.IntakeSource,
			Mail:          deps.Mail,
			Emit:          rec.emitter(emit),
			Interval:      cfg.GHIntake.Interval,
			Grace:         cfg.GHIntake.Grace,
			RenotifyAfter: cfg.GHIntake.RenotifyAfter,
			NotifyTo:      cfg.GHIntake.NotifyTo,
			EscalateAfter: cfg.GHIntake.EscalateAfter,
			EscalateTo:    escalateTo,
		})
		out.Intake = sample(out.Intake, w, rec, now, force)
	}

	// Carrier re-read.
	out.CarrierDrift = arm(prev.CarrierDrift,
		DecideArming(cfg.CarrierDrift.Enabled, ghOnPath, true, deps.CredentialOK), deps.Credential)
	if out.CarrierDrift.Arming == Armed {
		rec := &recorder{}
		snap, source := deps.DriftSnapshot, deps.DriftSource
		w := carrierdrift.New(carrierdrift.Options{
			Enabled: true,
			Agent:   Agent,
			Source: func() ([]carrierdrift.Carrier, int, error) {
				cs, n, err := source()
				for _, c := range cs {
					rec.carrier(c.Ref(), c.ID)
				}
				return cs, n, err
			},
			Snapshot: func(repo string, number int) (carrierdrift.Snapshot, error) {
				s, err := snap(repo, number)
				rec.lookup(fmt.Sprintf("%s#%d", repo, number), string(s.State), err)
				return s, err
			},
			Statuses: deps.DriftStatuses,
			Mail:     deps.Mail,
			Emit:     rec.emitter(emit),
			Interval: cfg.CarrierDrift.Interval,
			Windows: carrierdrift.Windows{
				Ack:    cfg.CarrierDrift.AckWindow,
				Stage:  cfg.CarrierDrift.StageWindow,
				Closed: cfg.CarrierDrift.ClosedGrace,
				Stages: cfg.CarrierDrift.Stages,
			},
			RenotifyAfter: cfg.CarrierDrift.RenotifyAfter,
			NotifyTo:      cfg.CarrierDrift.NotifyTo,
			EscalateAfter: cfg.CarrierDrift.EscalateAfter,
			EscalateTo:    escalateTo,
		})
		out.CarrierDrift = sample(out.CarrierDrift, w, rec, now, force)
	}

	out.FinishedAt = time.Now()
	if out.FinishedAt.Before(now) {
		out.FinishedAt = now
	}
	return out
}

// watcher is what the three watcher types have in common.
type watcher interface {
	Check(time.Time)
	State() watchstate.State
	Restore(watchstate.State)
}

func sample(d Detector, w watcher, rec *recorder, now time.Time, force bool) Detector {
	st := d.Watcher.Clone()
	if force {
		st.Ran = false
	}
	w.Restore(st)
	w.Check(now)
	after := w.State()
	d.Watcher = after
	if !after.Ran || !after.LastRun.Equal(now) {
		// Throttled: the interval since the last sample has not elapsed.
		return d
	}
	d.SampledThisRun = true
	d.LastSampledAt = now
	d.LastEvent, d.LastDetails = rec.last()
	if d.LastEvent == "" {
		d.LastEvent = "clean (no event)"
	}
	d.Lookups = rec.lookups
	return d
}

// recorder captures what one sample emitted and looked up.
type recorder struct {
	mu      sync.Mutex
	event   string
	details map[string]any
	lookups []Lookup
	// carriers maps "owner/repo#n" to the carrier ids that reference it, so a
	// lookup (which is keyed by issue alone) can name its carrier.
	carriers map[string][]string
}

func (r *recorder) carrier(issue, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.carriers == nil {
		r.carriers = map[string][]string{}
	}
	r.carriers[issue] = append(r.carriers[issue], id)
}

func (r *recorder) emitter(next func(events.Event)) func(events.Event) {
	return func(e events.Event) {
		r.mu.Lock()
		r.event, r.details = e.EventType, e.Details
		r.mu.Unlock()
		next(e)
	}
}

func (r *recorder) lookup(issue, state string, err error) {
	l := Lookup{Issue: issue, State: state}
	if err != nil {
		l.Error = err.Error()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l.Carrier = strings.Join(r.carriers[issue], ",")
	r.lookups = append(r.lookups, l)
}

func (r *recorder) last() (string, map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.event, r.details
}

// Read loads the File for home. A missing file is (File{}, os.ErrNotExist-wrapped
// error) — a job that has never run, which a reader must not treat as clean.
func Read(home string) (File, error) {
	var f File
	b, err := os.ReadFile(StatePath(home))
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("parsing %s: %w", StatePath(home), err)
	}
	if f.SchemaVersion != SchemaVersion {
		return File{}, fmt.Errorf("%s: schema_version %d, this build reads %d",
			StatePath(home), f.SchemaVersion, SchemaVersion)
	}
	return f, nil
}

// Write persists f atomically.
func Write(home string, f File) error {
	return writeJSON(StatePath(home), f)
}

// Caches are the mg-scan caches carried between runs.
type Caches struct {
	Intake       *ghintake.RefCache      `json:"intake"`
	CarrierDrift *carrierdrift.ItemCache `json:"carrier_drift"`
}

// ReadCaches loads the persisted caches, returning empty ones when there are
// none or they cannot be read — a cold cache costs forks, never correctness.
func ReadCaches(home string) Caches {
	c := Caches{Intake: ghintake.NewRefCache(), CarrierDrift: carrierdrift.NewItemCache()}
	b, err := os.ReadFile(filepath.Join(Dir(home), CacheFileName))
	if err != nil {
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Caches{Intake: ghintake.NewRefCache(), CarrierDrift: carrierdrift.NewItemCache()}
	}
	if c.Intake == nil {
		c.Intake = ghintake.NewRefCache()
	}
	if c.CarrierDrift == nil {
		c.CarrierDrift = carrierdrift.NewItemCache()
	}
	return c
}

// WriteCaches persists c atomically.
func WriteCaches(home string, c Caches) error {
	return writeJSON(filepath.Join(Dir(home), CacheFileName), c)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
