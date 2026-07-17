package main

// End-to-end tests for `pogo agent spawn-polecat --body-file` (mg-8380), the
// pogo half of mg-7850's fix. The claim under test is narrow and total: the
// file's bytes reach the polecat's generated prompt unexpanded and
// byte-identical, no matter what metacharacters they carry.
//
// These drive the REAL compiled binary through a REAL shell against a stub
// pogod, then render the captured body through the REAL template expander that
// pogod uses (agent.ExpandTemplateToFile) and read the resulting prompt file
// off disk. That covers the whole path the ticket cares about: shell → CLI →
// API request → prompt file. exec.Command alone would prove nothing here — it
// never invokes a shell, so the hazard would be absent and the test would pass
// whether or not --body-file works.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// hazardBody carries exactly the constructs the shell eats inside --body="...":
// backticked terms, $VAR, and $(cmd). This is not a contrived fixture — it is
// the shape of a real dispatch body. Mayor writes file:symbol cites, shell
// snippets and $(dirname ...) recipes into dispatch bodies constantly, and that
// is precisely the content that contains metacharacters. The bodies most worth
// writing exactly are the ones most exposed.
const hazardBody = "Cite symbols, not lines: `bodyFromFlags` in `cmd/pogo/bodyfile.go`.\n" +
	"Run $(git rev-parse --show-toplevel) and guard with || [ -n \"$line\" ].\n" +
	"Set $HOME/$UNSET_VAR, then `pogo agent spawn-polecat` with `--body-file`.\n"

// spawnCapture is a stub pogod that records the decoded spawn request and
// answers 201, so a test can inspect exactly what bytes the CLI transmitted.
type spawnCapture struct {
	req agent.SpawnPolecatAPIRequest
	got bool
}

func (c *spawnCapture) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&c.req); err != nil {
			t.Errorf("stub pogod decoding spawn request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		c.got = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agent.AgentInfo{Name: c.req.Name, PID: 4242})
	}
}

// runShPogo runs a pogo command line through /bin/sh against a stub pogod and
// returns the captured spawn request plus the exit code. The shell is the whole
// point: it is the component that mangles the body on the inline path.
func runShPogo(t *testing.T, cap *spawnCapture, stdin string, line string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ts := httptest.NewServer(cap.handler(t))
	t.Cleanup(ts.Close)
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	cmd := exec.Command("/bin/sh", "-c", line)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"POGO_PORT=" + strconv.Itoa(port),
		"HOME=" + t.TempDir(),
		"XDG_CONFIG_HOME=" + t.TempDir(),
		// Clear any ambient POGO_HOME so the CLI's state dir stays under the
		// temp HOME above instead of the developer's real ~/.pogo (mg-3dc3).
		"POGO_HOME=",
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running sh -c %q: %v", line, err)
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// renderPromptFile expands the real polecat-shaped template with the given body
// through the same expander pogod uses, writes it to a real file, and returns
// the file's contents. The template basename is deliberately unique so
// LoadDropIns cannot pick up the developer's real ~/.pogo drop-ins.
func renderPromptFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat-bodyfile-fixture.md")
	if err := os.WriteFile(tmplPath, []byte("Task: {{.Task}} ({{.Id}})\n\n{{.Body}}\n"), 0o644); err != nil {
		t.Fatalf("writing template: %v", err)
	}
	path, err := agent.ExpandTemplateToFile(tmplPath, agent.TemplateVars{
		Task: "prove the body arrives verbatim",
		Id:   "mg-8380",
		Body: body,
	})
	if err != nil {
		t.Fatalf("expanding template: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated prompt %s: %v", path, err)
	}
	return string(data)
}

func writeBodyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestSpawnPolecat_BodyFileVerbatimThroughShell is the ticket's central claim:
// a dispatch body full of shell metacharacters, passed with --body-file THROUGH
// A REAL SHELL, reaches the generated prompt file unexpanded and byte-identical.
//
// The --body control at the bottom is what gives this test teeth. It proves the
// shell really does mangle this body on the inline path, so the --body-file
// assertion passes because --body-file bypasses the shell — not because the
// fixture happened to be inert. Delete the file-reading path and this goes red.
func TestSpawnPolecat_BodyFileVerbatimThroughShell(t *testing.T) {
	path := writeBodyFile(t, t.TempDir(), "task.md", hazardBody)

	var fileCap spawnCapture
	_, stderr, code := runShPogo(t, &fileCap, "",
		pogoBin+" agent spawn-polecat cat-8380 --id mg-8380 --body-file "+path)
	if code != 0 {
		t.Fatalf("spawn --body-file must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !fileCap.got {
		t.Fatal("stub pogod never received a spawn request")
	}

	// The CLI must transmit the file's bytes unchanged...
	if fileCap.req.Body != hazardBody {
		t.Errorf("--body-file must transmit the file's bytes verbatim.\n want: %q\n  got: %q",
			hazardBody, fileCap.req.Body)
	}

	// ...and they must survive template expansion into the prompt the polecat
	// actually reads. This is the assertion the ticket asks for by name.
	prompt := renderPromptFile(t, fileCap.req.Body)
	if !strings.Contains(prompt, hazardBody) {
		t.Errorf("generated prompt file must contain the body verbatim.\n want substring: %q\n prompt: %q",
			hazardBody, prompt)
	}
	for _, frag := range []string{"`bodyFromFlags`", "$(git rev-parse --show-toplevel)", "$HOME/$UNSET_VAR", `|| [ -n "$line" ]`} {
		if !strings.Contains(prompt, frag) {
			t.Errorf("prompt lost %q — the body did not arrive unexpanded", frag)
		}
	}

	// The control: assert the inline path DID get mangled by the shell. If this
	// ever fails, the fixture stopped being hazardous and the check above is no
	// longer proving anything — fix the fixture, do not delete this.
	var inlineCap spawnCapture
	_, stderr, code = runShPogo(t, &inlineCap, "",
		pogoBin+` agent spawn-polecat cat-8380 --id mg-8380 --body="`+hazardBody+`"`)
	if code != 0 {
		t.Fatalf("spawn --body must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if inlineCap.req.Body == hazardBody {
		t.Fatal("control failed: the shell did not mangle --body, so this test cannot prove --body-file bypasses it")
	}
}

// TestSpawnPolecat_BodyStillWorksUnchanged proves the new path is conditional,
// not hard-wired: --body is untouched and NOT deprecated. A body with no
// metacharacters must still arrive intact on the inline path, exactly as before.
func TestSpawnPolecat_BodyStillWorksUnchanged(t *testing.T) {
	const inert = "fix the gradient on the DISPATCH channel"

	var cap spawnCapture
	_, stderr, code := runShPogo(t, &cap, "",
		pogoBin+` agent spawn-polecat cat-8380 --id mg-8380 --body="`+inert+`"`)
	if code != 0 {
		t.Fatalf("spawn --body must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if cap.req.Body != inert {
		t.Errorf("--body must still deliver its text unchanged.\n want: %q\n  got: %q", inert, cap.req.Body)
	}
}

// TestSpawnPolecat_BodyFileMissingFailsLoudly locks the half that matters most:
// an unreadable path must fail loudly, never spawn a polecat with an empty
// body. Silently dispatching nothing on a typo'd path — and exiting 0 — would
// be this ticket's own disease: an instrument reporting success for work it did
// not do.
func TestSpawnPolecat_BodyFileMissingFailsLoudly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")

	var cap spawnCapture
	stdout, stderr, code := runShPogo(t, &cap, "",
		pogoBin+" agent spawn-polecat cat-8380 --id mg-8380 --body-file "+missing)

	if code == 0 {
		t.Error("an unreadable --body-file must exit nonzero, got 0")
	}
	if cap.got {
		t.Error("an unreadable --body-file must not spawn a polecat at all — it spawned one with an empty body")
	}
	if !strings.Contains(stderr, "cannot read --body-file") {
		t.Errorf("failure must name the unreadable file on stderr, got stderr=%q", stderr)
	}
	if strings.Contains(stdout, "Spawned") {
		t.Errorf("failed spawn must not report success on stdout, got stdout=%q", stdout)
	}
}

// TestSpawnPolecat_BodyAndBodyFileMutuallyExclusive: passing both is a
// malformed invocation, and guessing which one the caller meant is exactly the
// silent-wrong-answer this ticket exists to prevent.
func TestSpawnPolecat_BodyAndBodyFileMutuallyExclusive(t *testing.T) {
	path := writeBodyFile(t, t.TempDir(), "task.md", "from the file")

	var cap spawnCapture
	_, stderr, code := runShPogo(t, &cap, "",
		pogoBin+` agent spawn-polecat cat-8380 --body="inline" --body-file `+path)

	if code == 0 {
		t.Error("passing both --body and --body-file must exit nonzero, got 0")
	}
	if cap.got {
		t.Error("passing both flags must not spawn a polecat")
	}
	if !strings.Contains(stderr, "cannot use both --body and --body-file") {
		t.Errorf("expected mutual-exclusion error on stderr, got stderr=%q", stderr)
	}
}

// TestSpawnPolecat_BodyFileStdin covers the "-" spelling mg already has, so the
// two CLIs don't diverge on it.
func TestSpawnPolecat_BodyFileStdin(t *testing.T) {
	var cap spawnCapture
	_, stderr, code := runShPogo(t, &cap, hazardBody,
		pogoBin+" agent spawn-polecat cat-8380 --id mg-8380 --body-file -")
	if code != 0 {
		t.Fatalf("spawn --body-file - must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if cap.req.Body != hazardBody {
		t.Errorf("--body-file - must read stdin verbatim.\n want: %q\n  got: %q", hazardBody, cap.req.Body)
	}
}

// TestSpawnPolecat_BodyFileHelpMatchesMg guards the wording contract with mg
// (mg-7850/b4eb43e): two binaries with the same hazard must not grow two
// spellings of the cure. A caller who learns --body-file on mg should not have
// to learn it again here.
func TestSpawnPolecat_BodyFileHelpMatchesMg(t *testing.T) {
	out, err := exec.Command(pogoBin, "agent", "spawn-polecat", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, out)
	}
	help := string(out)
	for _, want := range []string{
		"--body-file",
		`("-" for stdin)`,
		"mutually exclusive with --body",
		"verbatim",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("spawn-polecat --help must mention %q (mg parity), got:\n%s", want, help)
		}
	}
}
