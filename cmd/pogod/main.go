////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import _ "net/http/pprof"
import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/nightlyone/lockfile"
	"golang.org/x/net/netutil"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/health"
	"github.com/drellem2/pogo/internal/heartbeat"
	"github.com/drellem2/pogo/internal/pathenv"
	"github.com/drellem2/pogo/internal/platform/sleep"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/providers"
	"github.com/drellem2/pogo/internal/reaper"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/search"
	"github.com/drellem2/pogo/internal/server"
	"github.com/drellem2/pogo/internal/service"
	"github.com/drellem2/pogo/internal/stallwatch"
	"github.com/drellem2/pogo/internal/workitem"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

var agentRegistry *agent.Registry
var mergeQueue *refinery.Refinery
var sched *scheduler.Scheduler
var srv *server.Server
var startTime time.Time

var bindFlag = flag.String("bind", "", "address to bind the server to (default: 127.0.0.1)")
var portFlag = flag.Int("port", 0, "port to listen on (default: 10000)")

// maxHTTPConns caps concurrent HTTP connections so a client leak can't
// exhaust daemon file descriptors. Generous for a localhost daemon whose
// normal load is a handful of CLI and agent clients.
const maxHTTPConns = 256

// registryLiveness implements scheduler.AgentLiveness against the agent
// registry so the scheduler can garbage-collect mail-check-* schedules whose
// target agent has vanished (gh drellem2/macguffin #15). A schedule addresses an
// agent by its event identity (cat-/crew-<name>) or, for some crew schedules,
// its bare name, so we match on both. An agent counts as alive when its process
// is running, or when it's a restart-on-crash agent the registry still holds —
// a transient mid-restart window must not reap its mail-check loop. Anything
// else — no registry entry (stopped, or pogod restarted and lost its children),
// or a terminally-exited no-restart agent — is gone.
type registryLiveness struct{ reg *agent.Registry }

func (l registryLiveness) IsAlive(scheduleAgent string) bool {
	if l.reg == nil {
		return false
	}
	for _, a := range l.reg.List() {
		if a.Name == scheduleAgent || a.EventAgent() == scheduleAgent {
			return a.Alive() || a.RestartOnCrash
		}
	}
	return false
}

// schedulePauser implements agent.SchedulePauser against the scheduler so
// park can remove an agent's schedules (recording them in the park file for
// restore) and wake can re-add them (mg-41e1). Entries travel as raw JSON
// because the agent package cannot import the scheduler package (the
// scheduler already imports agent).
type schedulePauser struct{ sched *scheduler.Scheduler }

func (p schedulePauser) PauseForAgent(aliases ...string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, alias := range aliases {
		for _, e := range p.sched.List(alias) {
			if _, err := p.sched.Remove(e.Agent, e.ID); err != nil {
				return out, err
			}
			data, err := json.Marshal(e)
			if err != nil {
				return out, err
			}
			out = append(out, data)
		}
	}
	return out, nil
}

func (p schedulePauser) RestoreForAgent(entries []json.RawMessage) (int, error) {
	restored := 0
	var firstErr error
	now := time.Now()
	for _, raw := range entries {
		var e scheduler.Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Recompute the next fire for recurring entries — the recorded
		// NextFire likely came due during the park and must not replay as a
		// missed fire. One-shots keep their fire time on purpose: a gate-lift
		// reminder that came due while parked should fire once on wake.
		if !e.OneShot {
			e.NextFire = time.Time{}
		}
		if _, err := p.sched.Add(e, now); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		restored++
	}
	return restored, firstErr
}

// mailCheckRegistrar implements agent.MailCheckRegistrar against the scheduler
// so spawn-polecat can auto-register a polecat's mail-check loop at spawn time
// (mg-e633). The entry is addressed to the polecat's bare registry name — the
// identity PogodDeliverer.Get resolves for PTY nudge delivery and the reap path
// (RemoveMailChecksForAgent) matches on exit — with a mail-check-<id> schedule
// id so the scheduler's stale-entry sweep leaves it alone (mg-8e5d). Replay
// policy "once" and nudge delivery mirror the crew-agent mail-check convention.
type mailCheckRegistrar struct {
	sched *scheduler.Scheduler
	// escalate, when set, nudges the mayor that a live polecat was left with no
	// mail-check reachability channel after verify+retry both failed. nil
	// disables escalation (tests). Called ONLY on the persistent post-retry
	// path — never for the benign startup nil-registrar (mg-6fe0).
	escalate func(agentName, scheduleID string)
}

// RegisterMailCheck adds the polecat's mail-check schedule, then VERIFIES it
// actually persisted and retries ONCE if not. A mail-check loop is a polecat's
// primary reachability channel, so "best-effort" is the wrong contract:
// Scheduler.Add's persist is a disk write that can transiently fail, and a
// silent drop leaves a live worker unreachable. The verify+retry recovers that
// transient persist-IO suspect; on a persistent failure it escalates to the
// mayor (a live polecat going dark) and returns the error so the agent layer
// records schedule_register_failed telemetry. It CANNOT recover a nil
// registrar — that path never reaches here, it is handled a layer up (mg-6fe0).
func (m mailCheckRegistrar) RegisterMailCheck(agentName, workItemID, cron, message string) error {
	if m.sched == nil {
		return nil
	}
	scheduleID := scheduler.MailCheckIDPrefix + workItemID
	entry := scheduler.Entry{
		Agent:        agentName,
		ID:           scheduleID,
		Cron:         cron,
		ReplayPolicy: scheduler.ReplayOnce,
		Delivery:     scheduler.DeliveryNudge,
		Message:      message,
	}

	err := m.addAndVerify(entry, agentName, scheduleID)
	if err == nil {
		return nil
	}
	// Retry once — recovers a transient persist-IO failure (Add rolls its own
	// memory state back on a persist error, so the retry re-adds cleanly).
	if err = m.addAndVerify(entry, agentName, scheduleID); err == nil {
		return nil
	}

	// Persistent after retry: a live polecat with no reachability channel.
	// Escalate to the mayor so a human/coordinator can intervene.
	if m.escalate != nil {
		m.escalate(agentName, scheduleID)
	}
	return err
}

// addAndVerify performs one Add followed by a Get to confirm the entry is
// actually present afterward (Add reports persist errors, but a defensive Get
// also catches a lost write / concurrent reap). Returns nil only when the entry
// is verified present.
func (m mailCheckRegistrar) addAndVerify(entry scheduler.Entry, agentName, scheduleID string) error {
	if _, err := m.sched.Add(entry, time.Now()); err != nil {
		return err
	}
	if _, ok := m.sched.Get(agentName, scheduleID); !ok {
		return fmt.Errorf("mail-check schedule %s for %s absent after Add", scheduleID, agentName)
	}
	return nil
}

// scheduleRegisterFailureReporter implements agent.ScheduleRegisterFailureReporter
// by writing schedule_register_failed telemetry to the scheduler's own-root
// events.log (logPath). It is wired EVEN WHEN scheduler.New fails — its whole
// reason to exist is to make the startup nil-registrar drop loud — so it carries
// the resolved own-root path directly rather than a *Scheduler (which may not
// exist). Event-only: escalation to the mayor is the registrar adapter's job on
// the persistent post-retry path, not this reporter's (mg-6fe0).
type scheduleRegisterFailureReporter struct{ logPath string }

func (r scheduleRegisterFailureReporter) ReportScheduleRegisterFailed(agentName, mailbox, reason string) {
	scheduler.EmitScheduleRegisterFailedTo(r.logPath, agentName, scheduler.MailCheckIDPrefix+mailbox, reason)
}

// schedulerStallWindows implements agent.StallScheduleProvider against the
// scheduler so diagnose can tell a cron-driven agent's by-design between-cron
// idle from a genuine wedge (mg-5b23). For each recurring cron schedule
// addressed to the agent it reports the schedule's last/next firing and the
// interval between firings; one-shot and unparseable schedules contribute
// nothing.
type schedulerStallWindows struct{ sched *scheduler.Scheduler }

func (p schedulerStallWindows) CronWindowsForAgent(agentIdentity string) []agent.CronWindow {
	if p.sched == nil {
		return nil
	}
	now := time.Now()
	// A schedule may address an agent by its event identity (crew-/cat-<name>)
	// or, for some crew schedules, its bare name — mirror registryLiveness and
	// match either. List filters on exact Agent, so query both forms.
	aliases := []string{agentIdentity}
	if bare := strings.TrimPrefix(strings.TrimPrefix(agentIdentity, "crew-"), "cat-"); bare != agentIdentity {
		aliases = append(aliases, bare)
	}
	var windows []agent.CronWindow
	for _, alias := range aliases {
		for _, e := range p.sched.List(alias) {
			if e.OneShot {
				continue
			}
			interval := e.CronInterval(now)
			if interval <= 0 {
				continue
			}
			windows = append(windows, agent.CronWindow{
				LastFire: e.LastFire,
				NextFire: e.NextFire,
				Interval: interval,
			})
		}
	}
	return windows
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /health")
	fmt.Fprintf(w, "pogo is up and bouncing")
}

// versionInfo is the JSON body of GET /version. It reports the RUNNING
// process's build identity — the axis bin/pogo-self-deploy needs for three-way
// drift detection (running vs installed binary vs main HEAD) per mg-6afa. The
// running revision must be self-reported: reading `go version -m ~/go/bin/pogod`
// gives the INSTALLED binary's revision, which diverges from the running one
// the instant `go install` rewrites that file underneath a live daemon.
type versionInfo struct {
	Revision  string `json:"revision"`   // vcs.revision embedded at build
	Time      string `json:"time"`       // vcs.time
	Modified  bool   `json:"modified"`   // vcs.modified (dirty tree at build)
	GoVersion string `json:"go_version"` // toolchain that built it
	StartTime string `json:"start_time"` // RFC3339 process start
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	info := versionInfo{StartTime: startTime.Format(time.RFC3339)}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Revision = s.Value
			case "vcs.time":
				info.Time = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func healthFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	// Pogod health
	mode := "full"
	if srv != nil {
		mode = srv.Mode().String()
	}
	pogodHealth := health.Pogod{
		Status: "ok",
		Uptime: time.Since(startTime).Truncate(time.Second).String(),
		Mode:   mode,
	}

	// Agents health
	var agentsHealth health.Agents
	if agentRegistry != nil {
		agents := agentRegistry.List()
		agentsHealth.Total = len(agents)
		agentsHealth.Details = make([]health.AgentDetail, len(agents))
		for i, a := range agents {
			info := agent.ExportInfo(a)
			detail := health.AgentDetail{
				Name:     info.Name,
				Status:   string(info.Status),
				Restarts: info.RestartCount,
				Uptime:   info.Uptime,
				ExitCode: info.ExitCode,
			}
			agentsHealth.Details[i] = detail
			switch info.Status {
			case "running":
				agentsHealth.Running++
			default:
				agentsHealth.Exited++
			}
		}
	}

	// Refinery health
	var refineryHealth health.Refinery
	if mergeQueue != nil {
		st := mergeQueue.GetStatus()
		refineryHealth.Enabled = st.Enabled
		refineryHealth.Running = st.Running
		refineryHealth.QueueLength = st.QueueLen
		refineryHealth.HistoryLength = st.HistoryLen
		refineryHealth.PollInterval = st.PollInterval

		// Count recent failures from history
		for _, mr := range mergeQueue.History() {
			if mr.Status == refinery.StatusFailed {
				refineryHealth.RecentFailures++
			}
		}
	}

	resp := health.FullResponse{
		Pogod:    pogodHealth,
		Agents:   agentsHealth,
		Refinery: refineryHealth,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func homePage(w http.ResponseWriter, r *http.Request) {
	// Only match the exact root path. In Go 1.22+ ServeMux, the "/{$}"
	// pattern restricts this to "/", but if registered as "/" (catch-all),
	// we must check manually to avoid swallowing unmatched routes with a
	// confusing 200 response instead of a proper 404.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(w, "greetings from pogo daemon")
}

func allProjects(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects")
	switch r.Method {
	case "GET", "":
		json.NewEncoder(w).Encode(project.Projects())
	case "DELETE":
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if project.Remove(req.Path) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"removed": true,
				"path":    req.Path,
			})
		} else {
			http.Error(w, "project not found", http.StatusNotFound)
		}
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

// clean normalizes an incoming visit path. It must not append a trailing
// separator: the path may name a file, and lstat("/repo/file.go/") fails
// with ENOTDIR (mg-88cc). project.Visit appends the separator to directory
// paths where it needs one.
func clean(path string) string {
	return filepath.Clean(path)
}

func file(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /file")
	switch r.Method {
	case "POST":
		decoder := json.NewDecoder(r.Body)
		var req project.VisitRequest
		decodeErr := decoder.Decode(&req)
		if decodeErr != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		req.Path = clean(req.Path)
		response, err := project.Visit(req)
		if err != nil {
			http.Error(w, err.Message, err.Code)
			return
		}
		json.NewEncoder(w).Encode(response)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugin(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugin")
	switch r.Method {
	case "GET":
		encodedPath := r.URL.Query().Get("path")
		path, err := url.QueryUnescape(encodedPath)
		if err != nil {
			fmt.Printf("Error urldecoding path variable: %v\n", err)
			return
		}
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		resp := (*plugin).Info()
		json.NewEncoder(w).Encode(resp)
		return
	case "POST":
		var reqObj pogoPlugin.DataObject
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&reqObj)
		if err != nil {
			fmt.Printf("Request could not be parsed.")
			http.Error(w, "", http.StatusBadRequest)
			return
		}

		path := reqObj.Plugin
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		respString := (*plugin).Execute(reqObj.Value)
		var respObj = pogoPlugin.DataObject{Value: respString}
		json.NewEncoder(w).Encode(respObj)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugins(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugins")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(driver.GetPluginPaths())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func projectById(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects/{projectId}")
	switch r.Method {
	case "GET":
		projectIdStr := r.PathValue("projectId")
		// If projectIdStr blank we look at the queryParameter 'path'
		if projectIdStr == "file" {
			projectPathStr := r.URL.Query().Get("path")
			// url decode projectIdStr
			path, err := url.QueryUnescape(projectPathStr)
			log.Printf("Path: %s\n", path)
			if err != nil {
				log.Printf("Error urldecoding projectIdStr: %v\n", err)
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			proj := project.GetProjectByPath(path)
			if proj == nil {
				http.Error(w, "", http.StatusNotFound)
				return
			}
			resp := project.GetProject(proj.Id)
			json.NewEncoder(w).Encode(resp)
			return
		}
		projectId, err := strconv.Atoi(projectIdStr)
		if err != nil {
			log.Printf("Error converting projectId to int: %v\n", err)
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		resp := project.GetProject(projectId)
		if resp == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func status(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /status")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(project.GetProjectStatuses())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func registerHandlers() {
	http.HandleFunc("/", homePage)
	http.HandleFunc("/file", file)
	http.HandleFunc("/projects/{projectId}", projectById)
	http.HandleFunc("/projects", allProjects)
	http.HandleFunc("/plugin", plugin)
	http.HandleFunc("/plugins", plugins)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/health/full", healthFull)
	http.HandleFunc("/version", versionHandler)
	http.HandleFunc("/status", status)
	http.HandleFunc("/workitems", workitem.HandleWorkItems)

	// Agent and refinery endpoints behind orchestration guard.
	// When the server is in index-only mode, these return 503.
	orchestrated := http.NewServeMux()
	agentRegistry.RegisterHandlers(orchestrated)
	if mergeQueue != nil {
		// Use a closure so handlers always resolve the current mergeQueue.
		// SetRefineryStarter swaps the package-level pointer on orchestration
		// restart; binding handlers to the original instance leaves
		// /refinery/queue serving stale data from the dead refinery (#9).
		refinery.RegisterHandlersFunc(orchestrated, func() *refinery.Refinery {
			return mergeQueue
		})
	} else {
		// Refinery is disabled via config — register stub handlers so
		// /refinery/* endpoints return a clear "disabled" error instead
		// of a confusing 404.
		refinery.RegisterDisabledHandlers(orchestrated)
	}
	if sched != nil {
		// Scheduler is part of the orchestration substrate — registering or
		// removing schedules requires pogod to be in the same mode that runs
		// the heartbeat tick.
		sched.RegisterHandlers(orchestrated)
	}
	if srv != nil {
		http.Handle("/agents/", srv.RequireOrchestration(orchestrated))
		http.Handle("/agents", srv.RequireOrchestration(orchestrated))
		http.Handle("/refinery/", srv.RequireOrchestration(orchestrated))
		http.Handle("/scheduler/", srv.RequireOrchestration(orchestrated))

		// Server mode endpoints (not guarded — always available)
		srv.RegisterHandlers(http.DefaultServeMux)
	} else {
		// No server coordinator — register directly
		http.Handle("/agents/", orchestrated)
		http.Handle("/agents", orchestrated)
		http.Handle("/refinery/", orchestrated)
		http.Handle("/scheduler/", orchestrated)
	}
}

// resolveAgentProvider maps a config provider id to its agent.Provider
// descriptor via the providers registry. An unknown id logs a warning and
// falls back to Claude so a stale or mistyped config never wedges daemon
// startup.
func resolveAgentProvider(id string) *agent.Provider {
	p, ok := providers.Resolve(id)
	if !ok {
		log.Printf("WARNING: unknown agent provider %q in config; falling back to %q",
			id, p.ID)
	}
	return p
}

// stallWatchArmed reports whether pogod should arm the passive stall watcher on
// this boot. It requires both the watcher being enabled AND a config file being
// present (cfg.Source set). The cfg.Source gate mirrors the prompt-refresh /
// crew-auto-start gate in main(): an unconfigured daemon never auto-starts a
// coordinator, so arming the watcher would nudge a coordinator (default
// "ringmaster") this process never launched — spurious nudges and durable-mail
// noise on every isolated/CI/sandbox daemon (gh drellem2/pogo #75). Only watch a
// coordinator the daemon would actually start.
func stallWatchArmed(cfg *config.Config) bool {
	return cfg.StallWatch.Enabled && cfg.Source != ""
}

// newStallNudger builds the stall watcher's delivery function, mirroring the
// scheduler's PogodDeliverer: when the target agent is running, deliver via the
// PTY in wait-idle mode; otherwise fall back to durable macguffin mail so the
// signal survives an offline recipient.
//
// The wait-idle mode is the load-bearing choice for gh drellem2/pogo #61: it
// blocks until the agent's PTY goes quiet before writing, so a BUSY agent is
// never interrupted mid-turn (the write lands at the next turn boundary) and an
// idle agent is woken at once. The priority wake reuses this exact nudger — it
// does not introduce a second, more aggressive delivery path — so it inherits
// the never-interrupt-a-busy-agent guarantee. Extracted from main so the
// wait-idle behavior is unit-testable (see main_stallnudger_test.go).
func newStallNudger(reg *agent.Registry, mail func(to, from, subject, body string) error) stallwatch.Nudger {
	return func(agentName, message string) error {
		if reg != nil {
			a := reg.Get(agentName)
			if a != nil && a.Status == agent.StatusRunning {
				return a.NudgeWithMode(message, agent.NudgeWaitIdle, agent.DefaultNudgeTimeout)
			}
		}
		return mail(agentName, "stall-watch", "stall-watch: work piling up", message)
	}
}

// newMailCheckReachabilityEscalator builds the mayor-nudge fired when a
// polecat's mail-check schedule could not be registered even after
// verify+retry (mg-6fe0). A live polecat with no mail-check loop has no
// proactive reachability channel — it will miss reviewer findings and
// re-review requests that drive the modify<->review loop — so this is a
// coordination alert, not a cosmetic one. Delivery mirrors newStallNudger:
// wait-idle PTY nudge when the mayor is running (never interrupts a busy turn),
// durable macguffin mail otherwise so the signal survives an offline mayor.
func newMailCheckReachabilityEscalator(reg *agent.Registry, coordinator string) func(agentName, scheduleID string) {
	return func(agentName, scheduleID string) {
		msg := fmt.Sprintf(
			"reachability alert: polecat %s could not register its mail-check schedule %s after verify+retry — "+
				"it has NO proactive mail channel and may miss reviewer findings / re-review requests. "+
				"Re-register it (`pogo schedule %s --cron \"*/10 * * * *\" --id %s ...`) or restart it.",
			agentName, scheduleID, agentName, scheduleID)
		if reg != nil {
			if a := reg.Get(coordinator); a != nil && a.Status == agent.StatusRunning {
				if err := a.NudgeWithMode(msg, agent.NudgeWaitIdle, agent.DefaultNudgeTimeout); err == nil {
					return
				}
			}
		}
		if err := client.SendMGMail(coordinator, "pogod", "polecat reachability alert", msg); err != nil {
			log.Printf("pogod: mail-check reachability escalation to %s failed: %v", coordinator, err)
		}
	}
}

// newStartVerifier builds the post-spawn start-verification query for the
// auto-renudge watcher (mg-feb3). It reports a polecat as "started" once its mg
// work item has left the available/ queue — the item's presence in available/
// is the HARD unstarted-signal the watcher gates its bare-CR renudge on. workRoot
// is the macguffin work directory (~/.macguffin/work); it scans only available/,
// so the check is cheap and never walks the unbounded done/ tree. A read error
// propagates so the watcher treats it as inconclusive rather than renudging a
// possibly-working agent.
func newStartVerifier(workRoot string) agent.StartVerifier {
	return func(workItemID string) (bool, error) {
		items, err := workitem.ListFrom(workRoot, "available")
		if err != nil {
			return false, err
		}
		for _, it := range items {
			if it.ID == workItemID {
				return false, nil // still available → not yet claimed → unstarted
			}
		}
		return true, nil // left the available queue → claimed → started
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `pogod — the pogo daemon.

Supervises agents as UNIX processes: the mayor (the coordinator), polecats
(disposable worker agents), and the refinery (the merge queue). Work items
and mail live in mg/macguffin (the task-store CLI).

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	startTime = time.Now()

	// Rotate the launchd-managed log before anything writes to it, then mark
	// the run boundary. launchd appends across restarts (so prior-run crash
	// evidence survives), and this startup rotation keeps the file bounded
	// while guaranteeing the previous run's tail is in pogod.log or
	// pogod.log.1 when a post-mortem needs it (mg-6d02). No-op unless
	// stderr actually is pogod.log — dev runs and pipe-captured spawns are
	// untouched.
	rotated, logPath, rotErr := service.RotatePogodLogIfNeeded()
	if rotErr != nil {
		log.Printf("pogod: log rotation failed (continuing): %v", rotErr)
	}
	if rotated {
		log.Printf("pogod: rotated %s (previous run's log is %s.1)", logPath, logPath)
	}
	log.Printf("pogod: starting (pid=%d)", os.Getpid())

	// Repair PATH before spawning anything. Under launchd/systemd pogod inherits
	// a minimal or empty PATH, which breaks bare-name subprocess lookups such as
	// the scheduler/refinery `mg mail send` fallback (mg-905f). Do this first so
	// every child spawned below resolves mg/gh/git without absolute paths.
	if err := pathenv.Ensure(); err != nil {
		fmt.Printf("Warning: could not augment PATH: %v\n", err)
	}

	// Ensure the state dir exists before anything writes into it — a fresh
	// or isolated POGO_HOME (mg-3dc3) starts empty and the lockfile create
	// below fails on a missing parent dir.
	if err := os.MkdirAll(config.PogoHome(), 0755); err != nil {
		fmt.Printf("Cannot create state dir %s: %v", config.PogoHome(), err)
		os.Exit(1)
	}

	// Acquire lockfile. The path is derived from POGO_HOME (see
	// config.LockfilePath), NOT os.TempDir(): $TMPDIR differs between the
	// launchd domain and a shell/agent, so a TempDir-based lock did not
	// prevent a second pogod from starting and racing the live daemon for
	// :10000, hanging up the agent fleet via SIGHUP (#22).
	lock, err := lockfile.New(config.LockfilePath())
	if err != nil {
		fmt.Printf("Cannot create lock. reason: %v", err)
		os.Exit(1)
	}

	if err = lock.TryLock(); err != nil {
		// Only one pogod may own a POGO_HOME at a time. Name the PID that
		// currently holds the lock so the operator can find the live daemon.
		holder := "an unknown pid"
		if p, gerr := lock.GetOwner(); gerr == nil {
			holder = fmt.Sprintf("pid %d", p.Pid)
		}
		// Shared refinery/queue counts across host + containerized clients are
		// by-design shared-POGO_HOME state, not two live daemons (which cannot
		// coexist — this path hard-exits). See docs/CONFIGURATION.md (mg-f227).
		fmt.Printf("Cannot acquire pogod lock %s: held by %s (reason: %v).\n"+
			"A single pogod owns each POGO_HOME; its refinery/queue state is "+
			"shared by design across every client on this POGO_HOME (mg-f227).\n",
			config.LockfilePath(), holder, err)
		os.Exit(1)
	}

	defer func() {
		if err := lock.Unlock(); err != nil {
			fmt.Printf("Cannot unlock %q, reason: %v", lock, err)
		}
	}()

	// Initialize agent registry. The socket dir hangs off PogoHome, not
	// os.TempDir(): $TMPDIR is per-user, so two daemons on distinct POGO_HOME
	// roots with identically-named agents used to share one socket file and
	// fight the mg-d216 supervisor over it forever (mg-8532).
	socketDir, insidePogoHome := config.AgentSocketDir()
	if insidePogoHome {
		// NewRegistry creates socketDir with mode 0700, and MkdirAll stamps that
		// mode on every parent it creates on the way down — including the agents/
		// dir the sockets share with the prompt files, which has always been
		// 0755. Create that parent first so a fresh POGO_HOME ends up with the
		// same layout as an existing one. socketDir itself still lands at 0700,
		// which is what an attach socket wants.
		if err := os.MkdirAll(agent.PromptDir(), 0755); err != nil {
			fmt.Printf("Cannot create agent state dir %s: %v\n", agent.PromptDir(), err)
			os.Exit(1)
		}
	} else {
		log.Printf("pogod: POGO_HOME %s is too deep to hold unix sockets (sun_path limit); "+
			"agent attach sockets live in %s instead — still unique to this POGO_HOME",
			config.PogoHome(), socketDir)
	}
	var initErr error
	agentRegistry, initErr = agent.NewRegistry(socketDir)
	if initErr != nil {
		fmt.Printf("Cannot create agent registry: %v\n", initErr)
		os.Exit(1)
	}
	defer agentRegistry.StopAll(5 * time.Second)

	// Load config early so we can use it for agent command setup
	cfg := config.Load()

	// Pin the frozen legacy role names BEFORE anything reads a role name off
	// cfg (mg-bc47). The guard used to run much further down, next to the
	// prompt refresh — correct logic, too late: config.Load fills an empty
	// [agents] coordinator/worker with the LIVE Default* consts, so the first
	// boot of a build that flipped those defaults (mg-ce47) resolved the NEW
	// names and acted on them — auto-started a coordinator named "ringmaster",
	// armed the stall watcher on it, addressed refinery mail to its mailbox —
	// in the same second it wrote "mayor" into config.toml. It self-healed on
	// the next restart, leaving boot 1's coordinator a stray agent with an
	// orphaned mailbox. Pinning here and re-loading means boot 1 already sees
	// the pinned names.
	cfg = pinAndResolveRoles(cfg)

	// Second, config-aware PATH repair pass: [agents] extra_path lets a
	// deployment point pogod at harness runtimes the automatic probe in
	// pathenv.Ensure misses (gh #25). Runs before any agent spawns.
	if err := pathenv.EnsureExtra(cfg.Agents.ExtraPath); err != nil {
		fmt.Printf("Warning: could not apply [agents] extra_path: %v\n", err)
	}

	// Apply index-scope limits from config (mg-d205).
	search.SearchService.SetMaxFilesPerTree(cfg.MaxFilesPerTree)
	project.SetIndexRoots(cfg.IndexRoots)

	// Configure agent command templates and the harness providers.
	agentRegistry.SetCommandConfig(&cfg.Agents)

	// Role names were resolved by pinAndResolveRoles above, before any consumer
	// could read one. cfg here is the post-pin config.
	coordinator := cfg.Agents.CoordinatorName()

	// Register every known harness provider into the registry, then set the
	// global default. Before mg-b31b a single provider was resolved here, once,
	// at startup; now the registry resolves a provider per spawn from the
	// precedence chain (--provider flag > provider: frontmatter > per-type
	// config > global default). That is what lets one Codex polecat run
	// alongside a Claude fleet with no pogod restart.
	//
	// Each provider carries its own lifecycle hooks (applied per-spawn off the
	// agent's resolved provider, not a registry global):
	//   - PostSpawnHook auto-accepts the harness's workspace/trust dialog.
	//   - SessionHook is the lifetime modal-dismissal watcher (mg-4421) that
	//     scans tee'd PTY output for the rating dialog and rate-limit-options
	//     modal and dismisses each via its menu keystroke. It survives
	//     schedule-substrate failures by living inside pogod's per-agent PTY
	//     goroutine — see mg-ef6b §7 / mg-5a3d §4.
	for _, p := range providers.All() {
		agentRegistry.RegisterProvider(p)
	}
	agentRegistry.SetDefaultProvider(cfg.Agents.Provider)

	// Wire the post-spawn start-verification watcher (mg-feb3): after the initial
	// nudge, pogod checks whether a polecat actually claimed its work item and, if
	// a concurrent-spawn init-stall swallowed the kickoff, re-delivers a bare
	// submit terminator. The macguffin work root mirrors the stall watcher's
	// default (~/.macguffin/work).
	if home, err := os.UserHomeDir(); err == nil {
		agentRegistry.SetStartVerifier(newStartVerifier(filepath.Join(home, ".macguffin", "work")))
	}

	// Validate the command binary for each agent type exists on PATH. Each type
	// can select a different provider via [agents.<type>] provider, so resolve
	// per type. An empty configured command means "use the provider's default
	// template". Dedupe so an identical command is only checked once.
	checkedCmds := map[string]bool{}
	for _, agentType := range []string{"crew", "polecat"} {
		typeProvider := resolveAgentProvider(cfg.Agents.AgentProvider(agentType))
		typeCmd := cfg.Agents.AgentCommand(agentType)
		if typeCmd == "" {
			typeCmd = typeProvider.CommandTemplate
		}
		if !checkedCmds[typeCmd] {
			agent.ValidateCommandBinary(typeCmd)
			checkedCmds[typeCmd] = true
		}
	}

	// Set up agent lifecycle callbacks. Restart vs. cleanup is now driven by
	// the agent's RestartOnCrash flag (resolved from prompt frontmatter, with
	// type-based defaults: crew=true, polecat=false). This preserves the
	// historical behavior — crew agents are still restarted, polecats are
	// still cleaned up — while letting users opt out per-agent.
	// Bounded backstop for --defer-done polecats (gh #81): when such a polecat
	// merges, OnMerged skips the auto-done/auto-stop and arms this instead, so
	// the polecat can finish its own post-merge flow. If it never ends its
	// lifecycle, the backstop reaps + escalates it — the OnExit hook below
	// disarms it on a clean exit. Escalation mails the mayor.
	deferBackstop := newDeferredBackstop(deferDoneBackstopTimeout, agentRegistry, func(mr *refinery.MergeRequest) {
		subject := fmt.Sprintf("DEFER-DONE BACKSTOP FIRED: polecat %s lingered post-merge", mr.Author)
		body := fmt.Sprintf("A --defer-done polecat merged but did not complete its lifecycle within %s.\n"+
			"pogod reaped the lingering process to free its slot (gh #34/#35).\n\n"+
			"Work item: %s\nBranch: %s\nMR: %s\n\n"+
			"The polecat never called `mg done` — verify the work item state and re-dispatch if its post-merge flow (PR creation, verify, mail) did not finish.",
			deferDoneBackstopTimeout, mr.Author, mr.Branch, mr.ID)
		if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
			log.Printf("refinery: failed to mail coordinator defer-done backstop escalation: %v", err)
		}
	})

	agentRegistry.SetOnExit(func(a *agent.Agent, err error) {
		// Disarm any defer-done backstop for this polecat: its process has
		// ended, so the slot is free and there is nothing left to reap (gh #81).
		// A no-op for the vast majority of agents, which are not --defer-done.
		deferBackstop.cancel(a.Name)

		if a.ShouldRespawn() {
			// Restart-on-crash agents: respawn after a short backoff so a
			// fast crash loop doesn't peg the daemon. The agent stays in
			// the registry and its worktree (if any) is preserved.
			log.Printf("agent %s (%s) exited unexpectedly, scheduling restart", a.Name, a.Type)
			go func() {
				time.Sleep(2 * time.Second)
				if _, rerr := agentRegistry.Respawn(a.Name); rerr != nil {
					log.Printf("agent %s: restart failed: %v", a.Name, rerr)
				}
			}()
		} else {
			if a.RestartOnCrash {
				// restart_on_crash is set but the agent is parked — the park
				// flag (written before the park stop) suppresses the respawn
				// and routes the exit through the cleanup path (mg-41e1).
				log.Printf("agent %s (%s) exited while parked; suppressing respawn", a.Name, a.Type)
			}
			// No-restart agents: clean up worktree (if any) and remove from
			// the registry. Polecats hit this path by default; a crew agent
			// with restart_on_crash=false in its prompt frontmatter also
			// lands here.
			//
			// This callback fires from waitAndHandle on EVERY process exit
			// — normal completion, crash, and force-stop alike (pogo agent
			// stop SIGTERMs then SIGKILLs; cmd.Wait returns either way) —
			// so the worktree cleanup below runs on abnormal exits, not
			// only clean ones. The single exit path no in-process callback
			// can cover, pogod dying mid-polecat, is the job of the gitgc
			// startup sweep. See mg-30d5 D3.
			log.Printf("agent %s (%s) exited, cleaning up", a.Name, a.Type)
			if a.WorktreeDir != "" {
				if err := gitgc.RemoveWorktree(a.SourceRepo, a.WorktreeDir); err != nil {
					log.Printf("agent %s: worktree cleanup failed: %v", a.Name, err)
				} else {
					log.Printf("agent %s: removed worktree %s", a.Name, a.WorktreeDir)
				}
			}
			a.Cleanup()
			agentRegistry.Remove(a.Name)
			// Eagerly reap this agent's mail-check loop so it stops firing the
			// moment the agent is gone, rather than on the next Tick sweep
			// (gh drellem2/macguffin #15). Match on both the bare name and the
			// cat-/crew- event identity a schedule may be addressed by.
			if sched != nil {
				if n := sched.RemoveMailChecksForAgent(time.Now(), a.Name, a.EventAgent()); n > 0 {
					log.Printf("agent %s: reaped %d stale mail-check schedule(s)", a.Name, n)
				}
			}
		}
	})

	// Start the heartbeat detector. It compares wall vs monotonic time on
	// each tick and emits a system_wake event if they diverge by more than
	// the configured threshold — catches host sleep, VM pause, NTP step.
	// See docs/sleep-resilience-design.md §1.
	hb := heartbeat.New()
	if cfg.Heartbeat.Interval > 0 {
		hb.Interval = cfg.Heartbeat.Interval
	}
	if cfg.Heartbeat.JumpThreshold > 0 {
		hb.Threshold = cfg.Heartbeat.JumpThreshold
	}

	// Start the scheduler. Schedules in ~/.pogo/schedules.json drive a
	// Tick() call from the heartbeat loop — wall-clock jumps are absorbed
	// for free because the scheduler stores absolute fire times and the
	// same goroutine handles both system_wake detection and the sweep.
	schedPath, err := scheduler.DefaultPath()
	if err != nil {
		log.Printf("pogod: scheduler disabled (cannot resolve home dir): %v", err)
	} else {
		// Wire the schedule-register failure reporter FIRST, independent of
		// whether the scheduler below actually loads. If scheduler.New fails, the
		// mail-check registrar is never installed and every polecat spawn takes
		// the nil-registrar path — the startup suspect this telemetry exists to
		// surface (mg-6fe0). The reporter targets the scheduler's own-root
		// events.log, resolvable from schedPath even without a live *Scheduler.
		agentRegistry.SetScheduleRegisterFailureReporter(
			scheduleRegisterFailureReporter{logPath: scheduler.EventLogPath(schedPath)})

		deliverer := &scheduler.PogodDeliverer{
			Registry: agentRegistry,
			Mail:     client.SendMGMail,
		}
		s, err := scheduler.New(schedPath, deliverer)
		if err != nil {
			log.Printf("pogod: scheduler load failed (%s): %v", schedPath, err)
		} else {
			// Install the liveness checker so Tick garbage-collects
			// mail-check-* schedules whose target agent has disappeared
			// (gh drellem2/macguffin #15). Backed by the agent registry.
			s.SetLiveness(registryLiveness{reg: agentRegistry})
			sched = s
			// Make diagnose cron-aware: a crew agent driven by a recurring cron
			// is idle by design between firings and must not be flagged as
			// stalled within one cron interval of its last firing (mg-5b23).
			agentRegistry.SetStallScheduleProvider(schedulerStallWindows{sched: s})
			// Let park/wake pause and restore an agent's schedules (mg-41e1).
			agentRegistry.SetSchedulePauser(schedulePauser{sched: s})
			// Auto-register a polecat's mail-check loop at spawn so review
			// loops round-trip without manual schedule registration (mg-e633).
			// On a persistent registration failure (verify+retry both failed),
			// escalate to the mayor: a live polecat with no reachability channel
			// is a coordination problem, not a cosmetic one (mg-6fe0).
			agentRegistry.SetMailCheckRegistrar(mailCheckRegistrar{
				sched:    s,
				escalate: newMailCheckReachabilityEscalator(agentRegistry, coordinator),
			})
			log.Printf("pogod: scheduler loaded from %s", schedPath)
		}
	}

	// Build the stall watcher (gh drellem2/macguffin #12): the pogod-side third
	// leg of the wedge-response triad. It rides the heartbeat loop and nudges
	// the mayor when work piles up behaviorally — available items left
	// unclaimed past a threshold, or unread mail accumulating — even while the
	// mayor's process looks healthy. Running here, in pogod's
	// guaranteed-independent heartbeat, is the whole point: a watcher inside the
	// mayor's own loop can't catch that loop silently skipping its check-work /
	// check-mail steps. See internal/stallwatch and docs/design/stall-watch-design.md.
	//
	// Gate arming on cfg.Source, exactly as prompt refresh and crew auto-start
	// are below (see the cfg.Source == "" branch): an unconfigured daemon never
	// auto-starts a coordinator, so arming the watcher would nudge a coordinator
	// (default "ringmaster") that this process never launched — spurious nudges
	// and durable-mail noise on every isolated/CI/sandbox daemon (gh drellem2/pogo
	// #75). Only watch a coordinator the daemon would actually start.
	var stallWatcher *stallwatch.Watcher
	if cfg.StallWatch.Enabled && cfg.Source == "" {
		log.Printf("pogod: no config file at %s; stall watcher not armed (no auto-started coordinator to nudge)", config.ConfigFilePath())
	}
	if stallWatchArmed(cfg) {
		stallWatcher = stallwatch.New(cfg.StallWatch, stallwatch.Options{
			Nudge: newStallNudger(agentRegistry, client.SendMGMail),
			// Let a priority wake short-circuit the ~30s heartbeat poll for a
			// prompt follow-up sweep (gh #61). hb.Nudge coalesces, so this can't
			// storm the loop; the priority cooldown bounds it to one extra tick
			// per wake.
			FastPoll: hb.Nudge,
		})
		log.Printf("pogod: stall watcher enabled (agent=%s item_age=%s mail_age=%s max_mail=%d cooldown=%s priority_wake=%t wake_delay=%s wake_cooldown=%s fast_priorities=%s)",
			cfg.StallWatch.Agent, cfg.StallWatch.UnclaimedItemAgeThreshold,
			cfg.StallWatch.UnreadMailAgeThreshold, cfg.StallWatch.MaxUnreadMailCount,
			cfg.StallWatch.NudgeCooldown, cfg.StallWatch.PriorityWakeEnabled,
			cfg.StallWatch.HighPriorityWakeDelay, cfg.StallWatch.HighPriorityWakeCooldown,
			strings.Join(cfg.StallWatch.FastPriorities, ","))
	}

	// Drive both heartbeat-piggybacked subsystems from a single OnTick. The
	// scheduler runs inline (it stores absolute fire times, so a clock jump is
	// absorbed in the same goroutine). The stall watcher runs in a goroutine so
	// a wait-idle nudge — which can block up to DefaultNudgeTimeout — never
	// delays the next tick or the scheduler sweep; its per-category cooldown and
	// internal mutex keep overlapping checks safe.
	// pogodHeartbeatPath is pogod's OWN heartbeat file. The tier-1 reaper can
	// supervise every com.pogo.* job EXCEPT pogod itself (a child agent cannot
	// reap its parent, and launchd will not — mg-50e0). Publishing pogod's
	// heartbeat here, on every heartbeat tick, gives an external human-held
	// check (the digest, or bridget once threading is on) a way to DETECT a
	// dead pogod. This is detection, not recovery: the known single point of
	// failure this tier explicitly leaves open. See docs/design/reaper-design.md.
	pogodHeartbeatPath := filepath.Join(config.PogoHome(), "health", "pogod.heartbeat")
	hb.OnTick = func(now time.Time) {
		if err := reaper.WriteHeartbeat(pogodHeartbeatPath); err != nil {
			log.Printf("pogod: failed to write own heartbeat %s: %v", pogodHeartbeatPath, err)
		}
		if sched != nil {
			sched.Tick(context.Background(), now)
		}
		if stallWatcher != nil {
			go stallWatcher.Check(now)
		}
	}

	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	go hb.Run(hbCtx)

	// Start the polecat git garbage collector: a startup sweep plus a
	// periodic ticker that deletes stale polecat-* branches and reclaims
	// leaked worktrees once their work items have concluded. mg-30d5.
	startGitGC(hbCtx, agentRegistry, cfg.GitGC)

	// Start the tier-1 heartbeat reaper: a goroutine (NOT a LaunchAgent — the
	// wedge in mg-50e0 means we cannot rely on being spawned) that kickstarts
	// declared launchd jobs whose heartbeat state file has gone stale. Liveness
	// is heartbeat freshness, never process existence. mg-d18b.
	startReaper(hbCtx, cfg.Reaper)

	// Optional platform-specific wake notifier — reduces wake-event latency
	// from up-to-Interval (~30s) down to <1s by short-circuiting the
	// heartbeat tick when the OS reports a wake. Strict performance
	// optimization: hb alone is correct; an error here is logged and we
	// continue. See internal/platform/sleep and
	// docs/sleep-resilience-design.md §5.
	if err := sleep.Watch(hbCtx, hb.Nudge); err != nil {
		log.Printf("pogod: platform sleep shim unavailable: %v (heartbeat-only wake detection still active)", err)
	}

	// Start plugins
	driver.Init()

	defer driver.Kill()
	defer project.SaveProjects()

	// Load project list from disk (fast, no indexing)
	project.Init()

	// Prune stale registry entries — nonexistent paths and ephemeral
	// worktrees — before any indexing runs (mg-d205).
	project.PruneRegistry()

	// Start refinery merge queue loop
	refineCfg := refinery.DefaultConfig()
	if cfg.Refinery.Enabled {
		if cfg.Refinery.PollInterval > 0 {
			refineCfg.PollInterval = cfg.Refinery.PollInterval
		}
		var refErr error
		mergeQueue, refErr = refinery.New(refineCfg)
		if refErr != nil {
			fmt.Printf("Warning: refinery failed to start: %v\n", refErr)
		} else {
			mergeQueue.SetOnSubmit(func(mr *refinery.MergeRequest) {
				unlinkSubmittedPolecatWorktree(agentRegistry, mr, refinery.UnlinkWorktree)
			})
			mergeQueue.SetOnMerged(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: merged %s (branch=%s, author=%s)", mr.ID, mr.Branch, mr.Author)

				// Event-driven polecat stop (gh #35): mark the work item
				// done and stop the merged polecat now instead of waiting
				// for the mayor's next coordination cycle. Run async — the
				// stop can block up to its SIGTERM timeout and this
				// callback fires on the refinery loop.
				go reapMergedPolecat(agentRegistry, mr, client.CompleteMGWorkItem, deferBackstop)

				// Mail the coordinator so it can archive the work item and
				// handle QA. The mayor's reap loop stays as a backstop for
				// polecats the event-driven stop above misses (e.g. a merge
				// resolved while pogod was down).
				subject := fmt.Sprintf("MERGED: %s (branch=%s)", mr.ID, mr.Branch)
				body := fmt.Sprintf("Merge request %s succeeded.\nBranch: %s\nAuthor: %s", mr.ID, mr.Branch, mr.Author)
				// Surface deploy failures to the mayor so the runtime gap (merged
				// commit but stale binary) gets remediated. The merge has already
				// landed — only the post-merge deploy hook failed.
				if mr.DeployError != "" {
					body += fmt.Sprintf("\nDeploy: FAILED — %s", mr.DeployError)
				}
				if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
					log.Printf("refinery: failed to mail coordinator: %v", err)
				}
			})
			mergeQueue.SetOnFailed(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: failed %s (branch=%s, author=%s, error=%s, failure_count=%d)", mr.ID, mr.Branch, mr.Author, mr.Error, mr.FailureCount)

				subject := fmt.Sprintf("MERGE FAILED: %s (branch=%s)", mr.ID, mr.Branch)
				body := fmt.Sprintf("Merge request %s failed.\nBranch: %s\nAuthor: %s\nError: %s\nGate output: %s\nConsecutive failures: %d", mr.ID, mr.Branch, mr.Author, mr.Error, mr.GateOutput, mr.FailureCount)

				// Mail the author agent so they can fix and resubmit.
				if mr.Author != "" {
					if err := client.SendMGMail(mr.Author, "refinery", subject, body); err != nil {
						log.Printf("refinery: failed to mail author %s: %v", mr.Author, err)
					}
				}

				// Mail the coordinator so they can re-dispatch if the author exited.
				if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
					log.Printf("refinery: failed to mail coordinator: %v", err)
				}

				// Escalation: if the failure threshold has been reached, send
				// a separate alert to the mayor so it can intervene (e.g. stop
				// the polecat, reassign the work item).
				if mr.ThresholdReached {
					escSubject := fmt.Sprintf("FAILURE THRESHOLD REACHED: %s (%d consecutive failures)", mr.Author, mr.FailureCount)
					escBody := fmt.Sprintf("Author %s has failed %d consecutive merge attempts.\nLatest MR: %s\nBranch: %s\nError: %s\nConsider stopping the polecat or reassigning the work item.",
						mr.Author, mr.FailureCount, mr.ID, mr.Branch, mr.Error)
					if err := client.SendMGMail(coordinator, "refinery", escSubject, escBody); err != nil {
						log.Printf("refinery: failed to mail coordinator escalation: %v", err)
					}
				}

				// Auto-reopen the work item so it moves back to claimed/ for retry.
				// This keeps the item assigned to the original polecat.
				// Polecats use their work item ID as the author field.
				if mr.Author != "" {
					if err := client.ReopenMGWorkItem(mr.Author); err != nil {
						log.Printf("refinery: failed to reopen work item %s: %v", mr.Author, err)
					} else {
						log.Printf("refinery: reopened work item %s after merge failure", mr.Author)
					}
				}
			})
			go mergeQueue.Start(context.Background())
			defer mergeQueue.Stop()
		}
	}

	// Initialize server coordinator
	srv = server.New(agentRegistry, mergeQueue)
	if mergeQueue != nil {
		onMerged := mergeQueue.OnMergedFunc()
		onFailed := mergeQueue.OnFailedFunc()
		onSubmit := mergeQueue.OnSubmitFunc()
		srv.SetRefineryStarter(func() (*refinery.Refinery, error) {
			// refineCfg carries StatePath, so the fresh instance loads the
			// state the outgoing one flushed in Stop() — an orchestration
			// restart no longer empties the merge queue.
			newRef, err := refinery.New(refineCfg)
			if err != nil {
				return nil, err
			}
			if onMerged != nil {
				newRef.SetOnMerged(onMerged)
			}
			if onFailed != nil {
				newRef.SetOnFailed(onFailed)
			}
			// Re-wire OnSubmit too — without it, submits after an
			// orchestration restart stop unlinking polecat worktrees.
			if onSubmit != nil {
				newRef.SetOnSubmit(onSubmit)
			}
			mergeQueue = newRef
			go mergeQueue.Start(context.Background())
			return newRef, nil
		})
	}

	// Register HTTP handlers
	registerHandlers()

	// Start the HTTP listener BEFORE background indexing so the server
	// is immediately responsive to API calls (especially agent management).
	if *bindFlag != "" {
		cfg.Bind = *bindFlag
	}
	if *portFlag != 0 {
		cfg.Port = *portFlag
	}
	addr := cfg.ListenAddr()
	ln, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		log.Fatalf("pogod: failed to listen on %s: %v", addr, listenErr)
	}
	fmt.Printf("pogod listening on %s\n", addr)

	// Now start background work: indexing and repo scanning.
	// The server is already accepting connections above.
	go func() {
		project.IndexAll()
		log.Printf("pogod: background project indexing complete")
	}()

	// Start the timer-driven incremental indexer: every index_interval it
	// scans index_roots for new repos and re-walks the registered projects
	// that are due. Per-project exponential backoff skips projects whose
	// content hasn't changed (up to 16× the base interval); a detected change
	// or a `pogo visit` resets a project to base cadence (mg-1236). The walk
	// itself is incremental — unchanged files cost only an Lstat. This
	// replaces the event-based filesystem watcher. See
	// docs/design/indexing-strategy.md and mg-5b0d.
	project.StartPeriodicIndexer(hbCtx, cfg.IndexInterval)

	// Prompt refresh and crew auto-start are gated on a config file existing.
	// A pogod with no config file is an unconfigured or deliberately isolated
	// instance (tests, CI, POGO_HOME sandboxes) — installing default prompts
	// and spawning a mayor from them would put an unrequested agent fleet on
	// the machine, and before mg-3dc3 an isolated daemon did exactly that,
	// racing the real crew. Orchestration is opt-in via config.toml.
	if cfg.Source == "" {
		log.Printf("pogod: no config file at %s; skipping prompt refresh and crew auto-start", config.ConfigFilePath())
	} else {
		// Refresh installed prompts from the embedded source before auto-starting
		// any agents. When a new pogo binary ships prompt updates, the live files
		// under $POGO_HOME/agents/ stay stale until something runs InstallPrompts —
		// previously only `pogo install` and `pogo agent prompt install`. Doing it
		// here means a daemon restart is enough to propagate updates, and the PMs
		// auto-started below pick up the latest prompts on the same boot. Hash
		// stamps make this a no-op when nothing changed.
		if installRes, err := agent.InstallPrompts(agent.InstallOpts{}); err != nil {
			log.Printf("pogod: prompt refresh failed: %v", err)
		} else if len(installRes.Updated) > 0 || len(installRes.Installed) > 0 {
			log.Printf("pogod: refreshed prompts (installed=%d updated=%d skipped=%d)",
				len(installRes.Installed), len(installRes.Updated), len(installRes.Skipped))
		}

		// The role-default pin used to live here, between prompt refresh and
		// auto-start. It now runs in pinAndResolveRoles(), immediately after
		// config.Load() — the prompts refreshed just above are synthesized with
		// the process-wide role names, and the sweep below auto-starts an agent
		// named after the coordinator, so both must see the PINNED names, not
		// the freshly-flipped Default* consts (mg-bc47).

		// Auto-start crew agents whose prompt frontmatter declares auto_start = true.
		// This replaces the manual `pogo agent start mayor` step on a fresh boot
		// and is idempotent — agents already registered (e.g. across pogod
		// restart-while-running) are skipped. [agents] autostart = false (or
		// POGO_AGENT_AUTOSTART=false) turns the whole sweep off for daemons
		// that are configured but must not spawn a fleet — sandboxes and
		// tests (mg-9a1c). Prompt refresh above still runs: it only writes
		// files, it doesn't start anything.
		if !cfg.Agents.AutoStart {
			log.Printf("pogod: crew auto-start disabled ([agents] autostart = false); not starting any agents")
		} else {
			for _, res := range agentRegistry.AutoStartAgents() {
				switch res.Status {
				case agent.AutoStartStatusStarted:
					log.Printf("pogod: auto-started %s", res.Name)
				case agent.AutoStartStatusSkippedRunning:
					log.Printf("pogod: %s already running, skipping auto-start", res.Name)
				case agent.AutoStartStatusFailed:
					log.Printf("pogod: auto-start of %s failed: %s", res.Name, res.Error)
				}
			}
		}
	}

	// Serve HTTP (blocks until shutdown). Explicit server instead of bare
	// http.Serve so a slow or hung client can't pin a goroutine forever,
	// with a connection cap for backpressure (gh #38). Localhost-only
	// today, so the values are generous; WriteTimeout must cover the
	// slowest handler (/agents/spawn-polecat does a git worktree add plus
	// agent startup).
	httpServer := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       1 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	log.Fatal(httpServer.Serve(netutil.LimitListener(ln, maxHTTPConns)))
}
