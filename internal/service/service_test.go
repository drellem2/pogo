package service

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

func TestLaunchdPlistTemplate(t *testing.T) {
	data := launchdData{
		Label:      "com.pogo.daemon",
		PogodPath:  "/Users/test/go/bin/pogod",
		LogDir:     "/Users/test/Library/Logs/pogo",
		Home:       "/Users/test",
		PogoHome:   "/Users/test/.pogo",
		PluginPath: "/Users/test/.pogo/plugin",
		Path:       "/Users/test/.local/bin:/usr/local/bin:/usr/bin:/bin",
	}

	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	result := buf.String()

	checks := []struct {
		name  string
		want  string
		isRaw bool
	}{
		{"label", "<string>com.pogo.daemon</string>", false},
		{"pogod path", "<string>/Users/test/go/bin/pogod</string>", false},
		{"RunAtLoad", "<key>RunAtLoad</key>", false},
		// KeepAlive must be unconditional <true/>, not <dict><SuccessfulExit>false</SuccessfulExit></dict> —
		// see the table in scripts/launchd/README.md for why.
		{"KeepAlive unconditional", "<key>KeepAlive</key>\n    <true/>", false},
		// ProcessType=Interactive prevents App Nap from throttling refinery polling and agent idle detection.
		{"ProcessType Interactive", "<key>ProcessType</key>\n    <string>Interactive</string>", false},
		{"log path", "<string>/Users/test/Library/Logs/pogo/pogod.log</string>", false},
		{"PATH env", "<key>PATH</key>", false},
		{"HOME env", "<key>HOME</key>\n        <string>/Users/test</string>", false},
		{"POGO_HOME env", "<key>POGO_HOME</key>\n        <string>/Users/test/.pogo</string>", false},
		{"POGO_PLUGIN_PATH env", "<key>POGO_PLUGIN_PATH</key>\n        <string>/Users/test/.pogo/plugin</string>", false},
	}
	for _, c := range checks {
		if !strings.Contains(result, c.want) {
			t.Errorf("plist missing %s: expected substring %q\ngot:\n%s", c.name, c.want, result)
		}
	}

	// Guard against regressing to the old conditional KeepAlive form.
	if strings.Contains(result, "SuccessfulExit") {
		t.Error("plist still uses conditional KeepAlive (SuccessfulExit) — must be unconditional <true/>")
	}
}

func TestLaunchdPathIncludesCommonDirs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	p := launchdPath()
	required := []string{".local/bin", "go/bin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"}
	for _, dir := range required {
		if !strings.Contains(p, dir) {
			t.Errorf("launchdPath() missing %q: %s", dir, p)
		}
	}
}

func TestLogDirUnderUserLibraryLogs(t *testing.T) {
	// logDir() = $HOME/Library/Logs/pogo (macOS-standard). Track HOME so a
	// test sandbox can redirect daemon logs without touching the real
	// ~/Library/Logs tree.
	t.Setenv("HOME", "/tmp/pogo-test-home")
	d := logDir()
	if d != "/tmp/pogo-test-home/Library/Logs/pogo" {
		t.Errorf("logDir() with HOME=/tmp/pogo-test-home should be /tmp/pogo-test-home/Library/Logs/pogo, got: %s", d)
	}
}

func TestSystemdUnitTemplate(t *testing.T) {
	data := systemdData{PogodPath: "/usr/local/bin/pogod"}

	tmpl, err := template.New("unit").Parse(systemdUnitTemplate)
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	result := buf.String()

	if !strings.Contains(result, "ExecStart=/usr/local/bin/pogod") {
		t.Error("unit missing ExecStart")
	}
	if !strings.Contains(result, "Restart=on-failure") {
		t.Error("unit missing Restart")
	}
	if !strings.Contains(result, "WantedBy=default.target") {
		t.Error("unit missing WantedBy")
	}
}

func TestStatusNotInstalled(t *testing.T) {
	// On a test machine, the service should not be installed
	installed, path := Status()
	if path == "" {
		t.Skip("unsupported OS for this test")
	}
	// Just verify it returns without panicking; installed state depends on environment
	_ = installed
}

func TestRenderLaunchdPlistDeterministic(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	// renderLaunchdPlist drives the diff-aware idempotency check — the
	// installer compares its output byte-for-byte against the on-disk
	// plist. Two consecutive renders against the same env must match
	// exactly, otherwise every install becomes a forced reload.
	t.Setenv("HOME", "/tmp/pogo-render-test")
	t.Setenv("POGO_HOME", "/tmp/pogo-render-test/.pogo")
	a, _, err := renderLaunchdPlist()
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	b, _, err := renderLaunchdPlist()
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if a != b {
		t.Fatalf("renderLaunchdPlist produced different output on consecutive calls\nfirst:\n%s\nsecond:\n%s", a, b)
	}
	if !strings.Contains(a, "/tmp/pogo-render-test/Library/Logs/pogo/pogod.log") {
		t.Errorf("rendered plist did not pick up HOME override: %s", a)
	}
}

func TestLogTailReadsLastBytes(t *testing.T) {
	// logTail() is the failure-mail body builder — mayor needs to see the
	// most recent log lines, not the first ones, when an install fails.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	logsDir := filepath.Join(dir, "Library", "Logs", "pogo")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a log file larger than logTailBytes so we exercise the seek path.
	logPath := filepath.Join(logsDir, "pogod.log")
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&sb, "line %04d filler\n", i)
	}
	expectedTail := "line 1999 filler\n"
	if err := os.WriteFile(logPath, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tail := logTail()
	if !strings.HasSuffix(tail, expectedTail) {
		t.Errorf("logTail did not end with most recent line; tail ends with: %q", tail[max(0, len(tail)-40):])
	}
	if len(tail) > logTailBytes+128 {
		t.Errorf("logTail returned %d bytes, expected ~%d", len(tail), logTailBytes)
	}
	if strings.Contains(tail, "line 0000 filler") {
		t.Error("logTail returned start of file instead of end")
	}
}

func TestLogTailMissingFile(t *testing.T) {
	// If the daemon never started, pogod.log won't exist. logTail must
	// return a human-readable note rather than panicking — the failure
	// mail still has to render.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	tail := logTail()
	if !strings.Contains(tail, "could not open") {
		t.Errorf("expected 'could not open' note for missing log, got: %s", tail)
	}
}

// listenLocal binds an ephemeral 127.0.0.1 port for tests that need to
// simulate a stuck or releasing port without touching the real pogod
// at :10000.
func listenLocal(t *testing.T) (net.Listener, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral: %v", err)
	}
	return l, l.Addr().String()
}

func TestDrainAddrReturnsImmediatelyWhenFree(t *testing.T) {
	// Bind then immediately close so we know the port is unbound but was
	// recently valid (mirrors the post-launchctl-unload state).
	l, addr := listenLocal(t)
	l.Close()

	start := time.Now()
	if err := drainAddr(addr, 2*time.Second); err != nil {
		t.Fatalf("drainAddr returned error when port is free: %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("drainAddr on free port took %s, expected <500ms", took)
	}
}

func TestDrainAddrTimesOutWhenHeld(t *testing.T) {
	// If something keeps holding the port past the timeout, the installer
	// must surface a clear error rather than blindly running launchctl
	// load — that's the failure mode that produced 3 silent install
	// retries on mg-9cdc.
	l, addr := listenLocal(t)
	defer l.Close()
	go acceptForever(l)

	start := time.Now()
	err := drainAddr(addr, 500*time.Millisecond)
	if err == nil {
		t.Fatal("drainAddr returned nil while a listener was holding the port")
	}
	if took := time.Since(start); took < 400*time.Millisecond {
		t.Errorf("drainAddr returned too fast (%s); should have polled until ~timeout", took)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error should mention the address %q; got: %v", addr, err)
	}
}

func TestDrainAddrDetectsLateRelease(t *testing.T) {
	// Realistic scenario: pogod has started shutting down but the kernel
	// hasn't released the listening socket yet. drainAddr must notice when
	// the port frees mid-poll and return nil.
	l, addr := listenLocal(t)
	go acceptForever(l)
	go func() {
		time.Sleep(300 * time.Millisecond)
		l.Close()
	}()
	if err := drainAddr(addr, 3*time.Second); err != nil {
		t.Errorf("drainAddr did not notice the port releasing: %v", err)
	}
}

// acceptForever drains incoming connections so DialTimeout completes
// successfully (rather than hanging with no Accept).
func acceptForever(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

func TestQuiesceCrewIsNoOpWhenPogodDown(t *testing.T) {
	// quiesceCrew is called as the first step of installLaunchd. When
	// pogod isn't running there's nothing to quiesce — it must not panic
	// or block. This guards against a regression where the function tries
	// to talk to pogod unconditionally and stalls the install.
	if c, err := net.DialTimeout("tcp", pogodPort, 100*time.Millisecond); err == nil {
		c.Close()
		t.Skip("pogod is running on this host — skipping pogod-down quiesce test")
	}
	done := make(chan struct{})
	go func() {
		quiesceCrew()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("quiesceCrew blocked for >5s with pogod down — should be a fast no-op")
	}
}

func TestParseLaunchctlListPIDSupervised(t *testing.T) {
	// When launchd is actively supervising the daemon, `launchctl list LABEL`
	// includes a numeric "PID" line. parseLaunchctlListPID must return that
	// PID with ok=true so installLaunchd's fast-path can short-circuit.
	output := `{
	"LimitLoadToSessionType" = "Aqua";
	"Label" = "com.pogo.daemon";
	"OnDemand" = false;
	"LastExitStatus" = 0;
	"PID" = 12345;
	"Program" = "/Users/test/go/bin/pogod";
};`
	pid, ok := parseLaunchctlListPID(output)
	if !ok {
		t.Fatal("expected PID to be detected in supervised output")
	}
	if pid != 12345 {
		t.Errorf("expected PID 12345, got %d", pid)
	}
}

func TestParseLaunchctlListPIDOrphan(t *testing.T) {
	// mg-2c55 reproduction: launchctl knows about the label (the plist is
	// loaded — `runs = 0`, "(never exited)") but no process is supervised
	// because launchd-pogod has never actually spawned. An orphan pogod
	// (PPID=1, started by crew-respawn outside launchd) is holding :10000
	// and answering /health. parseLaunchctlListPID must return ok=false
	// here so installLaunchd falls through to the orchestrated install
	// instead of treating the orphan as healthy launchd-pogod.
	output := `{
	"LimitLoadToSessionType" = "Aqua";
	"Label" = "com.pogo.daemon";
	"OnDemand" = false;
	"LastExitStatus" = 0;
	"Program" = "/Users/test/go/bin/pogod";
};`
	if pid, ok := parseLaunchctlListPID(output); ok {
		t.Errorf("expected no PID in orphan-state output (loaded but not supervising), got %d", pid)
	}
}

func TestParseLaunchctlListPIDEmpty(t *testing.T) {
	// `launchctl list` returns empty output (or "Could not find" stderr)
	// when the label is not registered at all. parseLaunchctlListPID must
	// not panic and must report ok=false.
	if _, ok := parseLaunchctlListPID(""); ok {
		t.Error("expected ok=false for empty output")
	}
	if _, ok := parseLaunchctlListPID("Could not find service \"com.pogo.daemon\" in domain for port"); ok {
		t.Error("expected ok=false for not-found output")
	}
}

func TestParseLaunchctlListPIDIgnoresNonPIDLines(t *testing.T) {
	// Defense against false positives: only the literal "PID" key counts.
	// LastExitStatus, anchor PID strings inside Program paths, etc. must
	// not be mistaken for the PID assignment.
	output := `{
	"Label" = "com.pogo.daemon";
	"LastExitStatus" = 0;
	"Program" = "/Users/test/PID/pogod";
};`
	if pid, ok := parseLaunchctlListPID(output); ok {
		t.Errorf("expected no PID, got %d (parser matched a non-PID line)", pid)
	}
}

// supervisedListOutput is the launchctl list snapshot when launchd is
// actively supervising the daemon — the "PID" key is present.
const supervisedListOutput = `{
	"LimitLoadToSessionType" = "Aqua";
	"Label" = "com.pogo.daemon";
	"OnDemand" = false;
	"LastExitStatus" = 0;
	"PID" = 12345;
	"Program" = "/Users/test/go/bin/pogod";
};`

// orphanListOutput is the launchctl list snapshot when the plist is
// loaded but no process is supervised by launchd. This is the mg-df4a
// regression state: an orphan pogod (started outside launchd by
// crew-respawn) holds :10000 and answers /health, but launchctl has no
// PID assigned. The fast-path must NOT short-circuit here — the
// orchestrated install must take over to replace the orphan.
const orphanListOutput = `{
	"LimitLoadToSessionType" = "Aqua";
	"Label" = "com.pogo.daemon";
	"OnDemand" = false;
	"LastExitStatus" = 0;
	"Program" = "/Users/test/go/bin/pogod";
};`

func TestCanSkipInstallSupervisedHappyPath(t *testing.T) {
	// Plist matches and launchctl is supervising — the only legitimate
	// no-op condition. Caller still gates on /health before short-circuiting.
	if !canSkipInstall(true, supervisedListOutput) {
		t.Error("expected canSkipInstall=true when plist matches and launchctl supervising")
	}
}

func TestCanSkipInstallOrphanRegression(t *testing.T) {
	// mg-df4a regression: pogod is running outside launchd (orphan, PPID=1
	// because launchd is PID 1 — not because the process is unparented),
	// /health succeeds, plist on disk matches the rendered template, and
	// launchctl knows the label. Pre-mg-2c55, all three of those signals
	// were enough to take the fast-path. The PID-assignment gate flips
	// the decision: launchctl reports no PID, so canSkipInstall must
	// return false and force the orchestrated install.
	if canSkipInstall(true, orphanListOutput) {
		t.Error("regression: canSkipInstall returned true for orphan state " +
			"(plist loaded, no PID assigned). Fast-path would silently no-op " +
			"on top of an orphan pogod and never replace it with a launchd-supervised one.")
	}
}

func TestCanSkipInstallPlistMismatch(t *testing.T) {
	// Even when launchctl is supervising, a plist diff (e.g. user upgraded
	// pogod and the path changed, or template fields drift) must force a
	// rewrite + reload. canSkipInstall must reject regardless of supervision.
	if canSkipInstall(false, supervisedListOutput) {
		t.Error("expected canSkipInstall=false when plist differs from rendered template")
	}
}

func TestCanSkipInstallNotRegistered(t *testing.T) {
	// First-ever install: launchctl returns empty (or "Could not find" on
	// stderr, normalized to empty by launchctlListOutputForLabel's err path).
	// Must reject so the orchestrated install runs.
	if canSkipInstall(true, "") {
		t.Error("expected canSkipInstall=false when launchctl has no record of the label")
	}
	if canSkipInstall(true, `Could not find service "com.pogo.daemon" in domain for port`) {
		t.Error("expected canSkipInstall=false on \"Could not find\" output")
	}
}

func TestIsLaunchdLoadedFromOutput(t *testing.T) {
	// Defense-in-depth for the loaded gate consumed by canSkipInstall AND
	// by installLaunchd's "if loaded → unload" branch. Both paths share
	// one launchctl snapshot now (mg-df4a), so a regression here would
	// silently break unload-before-reinstall too.
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"could not find", `Could not find service "com.pogo.daemon" in domain for port`, false},
		{"orphan (loaded but no PID)", orphanListOutput, true},
		{"supervised", supervisedListOutput, true},
	}
	for _, c := range cases {
		if got := isLaunchdLoadedFromOutput(c.in); got != c.want {
			t.Errorf("%s: isLaunchdLoadedFromOutput=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestKickstartLaunchdTargetFormat(t *testing.T) {
	// installLaunchd issues `launchctl kickstart -k <target>` after
	// `launchctl load` to defeat the "pended nondemand spawn = speculative"
	// state that otherwise leaves runs=0 indefinitely (mg-3963).
	// The target string MUST be `gui/<uid>/<label>` — without the gui/$UID
	// prefix kickstart fails with "Could not find specified service" and
	// the install regresses to the silent 10s timeout.
	tgt := kickstartLaunchdTarget()
	if !strings.HasPrefix(tgt, "gui/") {
		t.Errorf("kickstart target must start with gui/ to scope to the current user session domain; got %q", tgt)
	}
	if !strings.HasSuffix(tgt, "/"+launchdLabel) {
		t.Errorf("kickstart target must reference the %s label; got %q", launchdLabel, tgt)
	}
	// Catch the regression where someone splits the format and forgets a
	// segment (e.g. "gui//com.pogo.daemon" or "gui/501com.pogo.daemon").
	parts := strings.Split(tgt, "/")
	if len(parts) != 3 {
		t.Errorf("kickstart target must have exactly 3 slash-separated parts (gui/<uid>/<label>); got %q", tgt)
	}
	if len(parts) == 3 && parts[1] == "" {
		t.Errorf("kickstart target uid segment is empty; got %q", tgt)
	}
}

func TestKickstartTargetForLabel(t *testing.T) {
	// The reaper (mg-d18b) kickstarts jobs OTHER than pogod, so the target
	// builder must be label-parameterized and produce the same gui/<uid>/<label>
	// form the pogod path relies on.
	tgt := kickstartTargetForLabel("com.pogo.watchdog")
	parts := strings.Split(tgt, "/")
	if len(parts) != 3 || parts[0] != "gui" || parts[1] == "" || parts[2] != "com.pogo.watchdog" {
		t.Errorf("kickstartTargetForLabel = %q, want gui/<uid>/com.pogo.watchdog", tgt)
	}
	// And the pogod path must still route through the same builder.
	if kickstartLaunchdTarget() != kickstartTargetForLabel(launchdLabel) {
		t.Errorf("kickstartLaunchdTarget diverged from kickstartTargetForLabel(%s)", launchdLabel)
	}
}

func TestServicePaths(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		p := launchdPlistPath()
		if !strings.HasSuffix(p, "Library/LaunchAgents/com.pogo.daemon.plist") {
			t.Errorf("unexpected plist path: %s", p)
		}
	case "linux":
		p := systemdUnitPath()
		if !strings.HasSuffix(p, ".config/systemd/user/pogo.service") {
			t.Errorf("unexpected unit path: %s", p)
		}
	default:
		t.Skip("unsupported OS")
	}
}

// sandboxHome redirects HOME, XDG_CONFIG_HOME and POGO_HOME at fresh temp dirs
// and returns the POGO_HOME path. Both the developer shell and launchd export
// POGO_HOME on some machines, and config.toml is layered across HOME/XDG since
// mg-cf9e, so POGO_HOME alone would not isolate the write.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("POGO_HOME", state)
	return state
}

// The mg-bc47 role-migration bug, service-install mail side (mg-e545). On an
// existing install whose config.toml predates the [agents] role keys, the first
// process of a build that flipped the defaults (mg-ce47) would address the
// install report to the NEW default's mailbox nobody reads. installMailCoordinator
// must pin the frozen legacy name and resolve from the pinned config instead.
func TestInstallMailCoordinatorResolvesPinnedName(t *testing.T) {
	state := sandboxHome(t)

	cfgPath := filepath.Join(state, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agents]\nautostart = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bare literal, not config.DefaultCoordinator: comparing against the const
	// would follow a future flip instead of catching a regression.
	if got := installMailCoordinator(); got != "mayor" {
		t.Errorf("coordinator = %q, want pinned legacy %q — install mail would reach a mailbox nobody reads", got, "mayor")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `coordinator = "mayor"`) {
		t.Errorf("config.toml not pinned:\n%s", data)
	}
}

// A fresh install has no config.toml and adopts the live defaults; the pin is a
// no-op and writes nothing.
func TestInstallMailCoordinatorFreshInstallUsesDefault(t *testing.T) {
	state := sandboxHome(t)

	if config.IsExistingInstall() {
		t.Fatal("precondition: empty sandbox must not read as an existing install")
	}
	if got := installMailCoordinator(); got != config.DefaultCoordinator {
		t.Errorf("coordinator = %q, want live default %q on a fresh install", got, config.DefaultCoordinator)
	}
	if _, err := os.Stat(filepath.Join(state, "config.toml")); !os.IsNotExist(err) {
		t.Error("fresh install wrote config.toml; the pin must be a no-op")
	}
}
