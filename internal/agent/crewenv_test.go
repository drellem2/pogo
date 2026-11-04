package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// drellem2/pogo#263: crew prompt frontmatter `env`.

func parseFM(t *testing.T, fm string) *AgentMeta {
	t.Helper()
	meta, _, err := parsePromptFrontmatterBytes([]byte("+++\n" + fm + "+++\nbody\n"))
	if err != nil {
		t.Fatalf("parse %q: %v", fm, err)
	}
	return meta
}

func TestFrontmatterEnvSingleLine(t *testing.T) {
	meta := parseFM(t, `env = ["FOO=bar", "EMPTY=", "URL=http://x/?a=b#frag", "Q=say \"hi\" ]"]`+"\n")
	want := []string{"FOO=bar", "EMPTY=", "URL=http://x/?a=b#frag", `Q=say "hi" ]`}
	if !reflect.DeepEqual(meta.Env, want) {
		t.Errorf("Env = %q, want %q", meta.Env, want)
	}
	if !meta.HasField("env") {
		t.Error("HasField(env) = false, want true")
	}
	if got := meta.EnvKeys(); !reflect.DeepEqual(got, []string{"FOO", "EMPTY", "URL", "Q"}) {
		t.Errorf("EnvKeys = %q", got)
	}
	if w := meta.EnvWarnings(); len(w) != 0 {
		t.Errorf("unexpected warnings: %q", w)
	}
}

func TestFrontmatterEnvMultiLine(t *testing.T) {
	meta := parseFM(t, `auto_start = true
env = [
  "A=1",   # trailing comment
  # a comment line

  "B=2",
]
nudge_on_start = "go"
`)
	if !reflect.DeepEqual(meta.Env, []string{"A=1", "B=2"}) {
		t.Errorf("Env = %q", meta.Env)
	}
	if !meta.AutoStart || meta.NudgeOnStart != "go" {
		t.Errorf("neighbouring keys lost: AutoStart=%v NudgeOnStart=%q", meta.AutoStart, meta.NudgeOnStart)
	}
}

func TestFrontmatterEnvEmptyArray(t *testing.T) {
	meta := parseFM(t, "env = []\n")
	if len(meta.Env) != 0 || !meta.HasField("env") || len(meta.EnvWarnings()) != 0 {
		t.Errorf("env = []: Env=%q HasField=%v warnings=%q", meta.Env, meta.HasField("env"), meta.EnvWarnings())
	}
}

// A malformed env line must cost the env field and nothing else:
// startCrewAgent discards parse errors, so a block-level error would silently
// drop auto_start, restart_on_crash and nudge_on_start with it.
func TestFrontmatterEnvMalformedIsFieldScoped(t *testing.T) {
	cases := map[string]string{
		"bare string":           `env = "FOO=bar"`,
		"unquoted element":      `env = [FOO=bar]`,
		"missing comma":         `env = ["A=1" "B=2"]`,
		"unterminated string":   `env = ["A=1]`,
		"text after bracket":    `env = ["A=1"] junk`,
		"single quotes":         `env = ['A=1']`,
		"bad escape":            `env = ["A=\q"]`,
		"unterminated one-line": `env = ["A=1",`,
	}
	for name, envLine := range cases {
		t.Run(name, func(t *testing.T) {
			// Positive control: the same block with a valid env line parses
			// env, so an empty Env below is the malformation, not the harness.
			good := parseFM(t, "auto_start = true\nenv = [\"OK=1\"]\n")
			if len(good.Env) != 1 {
				t.Fatalf("control failed: Env = %q", good.Env)
			}

			meta := parseFM(t, "auto_start = true\nrestart_on_crash = false\n"+envLine+"\nnudge_on_start = \"go\"\nworktree = true\n")
			if !meta.AutoStart || meta.RestartOnCrash || !meta.HasField("restart_on_crash") ||
				meta.NudgeOnStart != "go" || !meta.Worktree {
				t.Errorf("malformed env took neighbours down: %+v", *meta)
			}
			if meta.Env != nil || meta.HasField("env") {
				t.Errorf("malformed env was applied: Env=%q HasField=%v", meta.Env, meta.HasField("env"))
			}
			w := meta.EnvWarnings()
			if len(w) != 1 || !strings.Contains(w[0], "line 3: env ignored") {
				t.Errorf("warnings = %q, want one naming line 3", w)
			}
		})
	}
}

// Review round 1 (mg-cbe8b): a malformed MULTI-LINE array must also cost only
// env — its own closing ']' must not be parsed as a key line and fail the
// block.
func TestFrontmatterEnvMalformedMultiLineIsFieldScoped(t *testing.T) {
	cases := map[string]string{
		"unquoted element":           "env = [\n  FOO=bar,\n]\n",
		"unquoted second element":    "env = [\"A=1\",\n  B=2\n]\n",
		"unquoted then trailing ]":   "env = [\n  \"A=1\",\n  B=2 ]\n",
		"known key interrupts array": "env = [\n  FOO=bar,\nworktree = true\n]\n",
		"unquoted, comment on close": "env = [\n  FOO=bar,\n] # end\n",
		"unquoted, no close at all":  "env = [\n  FOO=bar,\n",
		// Review round 2 (mg-cbe8b): an element ending in ']' must not be
		// taken for the array's end, and a line with no '=' in a broken
		// array is env's problem, not the block's.
		"element ending in ]":         "env = [\n  HOST=[::1]\n]\n",
		"no '=' and no ]":             "env = [\n  LOG_LEVEL\n",
		"junk after interrupting key": "env = [\n  FOO\nworktree = true\n  BAR\n]\n",
	}
	for name, envBlock := range cases {
		t.Run(name, func(t *testing.T) {
			meta, _, err := parsePromptFrontmatterBytes([]byte("+++\n" + envBlock +
				"auto_start = true\nrestart_on_crash = true\nnudge_on_start = \"go\"\n+++\nbody\n"))
			if err != nil {
				t.Fatalf("malformed multi-line env failed the whole block: %v", err)
			}
			if !meta.AutoStart || !meta.RestartOnCrash || meta.NudgeOnStart != "go" {
				t.Errorf("neighbours lost: %+v", *meta)
			}
			if strings.Contains(name, "key") && !meta.Worktree {
				t.Errorf("recognized key inside a broken array was swallowed")
			}
			w := meta.EnvWarnings()
			if meta.Env != nil || meta.HasField("env") || len(w) == 0 || !strings.Contains(w[0], "line 1: env ignored") {
				t.Errorf("Env=%q HasField=%v warnings=%q", meta.Env, meta.HasField("env"), w)
			}
		})
	}
}

// An unterminated multi-line array stops at the first line that cannot be an
// array line, so the keys after it survive.
func TestFrontmatterEnvUnterminatedArrayDoesNotSwallowKeys(t *testing.T) {
	meta := parseFM(t, "env = [\n  \"A=1\",\nauto_start = true\nnudge_on_start = \"go\"\n")
	if !meta.AutoStart || meta.NudgeOnStart != "go" {
		t.Errorf("keys after unterminated env lost: %+v", *meta)
	}
	if meta.Env != nil || len(meta.EnvWarnings()) != 1 {
		t.Errorf("Env=%q warnings=%q", meta.Env, meta.EnvWarnings())
	}
}

// A well-formed array drops only its bad entries, and a warning never quotes
// an entry with no '=' (it may be a secret).
func TestFrontmatterEnvBadEntriesSkipped(t *testing.T) {
	meta := parseFM(t, `env = ["GOOD=1", "hunter2", "1BAD=x", "=x", "BAD-NAME=y", "ALSO_GOOD=2"]`+"\n")
	if !reflect.DeepEqual(meta.Env, []string{"GOOD=1", "ALSO_GOOD=2"}) {
		t.Errorf("Env = %q", meta.Env)
	}
	w := meta.EnvWarnings()
	if len(w) != 4 {
		t.Fatalf("warnings = %q, want 4", w)
	}
	for _, line := range w {
		if strings.Contains(line, "hunter2") {
			t.Errorf("warning leaks an entry value: %q", line)
		}
	}
}

func TestFrontmatterNoEnv(t *testing.T) {
	meta := parseFM(t, "auto_start = true\n")
	if meta.Env != nil || meta.HasField("env") || meta.EnvWarnings() != nil || len(meta.EnvKeys()) != 0 {
		t.Errorf("no env key: %+v", *meta)
	}
}

// pm-pogo ruling on #263: POGO_* is reserved in crew env. The reserved key is
// skipped with a warning naming it; the rest of env and of the block applies.
func TestFrontmatterEnvReservesPogoPrefix(t *testing.T) {
	meta := parseFM(t, "auto_start = true\nenv = [\"POGO_ROLE=x\", \"FOO=bar\", \"POGO_HOME=/elsewhere\"]\nnudge_on_start = \"go\"\n")
	if !reflect.DeepEqual(meta.Env, []string{"FOO=bar"}) {
		t.Errorf("Env = %q, want [FOO=bar]", meta.Env)
	}
	if !meta.AutoStart || meta.NudgeOnStart != "go" || !meta.HasField("env") {
		t.Errorf("reserved key cost more than itself: %+v", *meta)
	}
	w := strings.Join(meta.EnvWarnings(), "\n")
	for _, k := range []string{"POGO_ROLE", "POGO_HOME"} {
		if !strings.Contains(w, `"`+k+`"`) {
			t.Errorf("no warning names %s: %q", k, meta.EnvWarnings())
		}
	}
	if strings.Contains(w, "/elsewhere") {
		t.Errorf("warning leaks a value: %q", w)
	}
	// Control: a near-miss name is not reserved.
	if m := parseFM(t, "env = [\"POGOX=1\", \"pogo_role=1\"]\n"); len(m.Env) != 2 {
		t.Errorf("only the exact POGO_ prefix is reserved: Env = %q", m.Env)
	}
}

// layerEnv's order is the contract: base beneath identity beneath spawn.
func TestSpawnBaseEnvBeneathIdentity(t *testing.T) {
	testsandbox.Isolate(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	dir := t.TempDir()
	spawnCapture := filepath.Join(dir, "spawn.env")
	respawnCapture := filepath.Join(dir, "respawn.env")

	a, err := reg.Spawn(SpawnRequest{
		Name:    "base-env",
		Type:    TypeCrew,
		Command: captureEnvCommand(spawnCapture),
		BaseEnv: []string{"POGO_AGENT_NAME=impostor", "BASE_ONLY=b", "BOTH=base"},
		Env:     []string{"BOTH=spawn"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitDone(t, a)
	a.Command = captureEnvCommand(respawnCapture)
	b, err := reg.Respawn("base-env")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)

	for what, path := range map[string]string{"spawn": spawnCapture, "respawn": respawnCapture} {
		env := readEnvCapture(t, path)
		want := map[string]string{"POGO_AGENT_NAME": "base-env", "BASE_ONLY": "b", "BOTH": "spawn"}
		for k, v := range want {
			if env[k] != v {
				t.Errorf("%s: %s = %q, want %q", what, k, env[k], v)
			}
		}
	}
}

// End to end through the crew start path: a crew prompt's env reaches the
// process on start and again on a crash restart, beneath identity.
func TestStartCrewAgentAppliesFrontmatterEnv(t *testing.T) {
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// The command template is a script that dumps its env to the next numbered
	// file and exits; it ignores whatever arguments the template appends.
	capDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "capture.sh")
	body := "#!/bin/sh\nn=$(ls '" + capDir + "' | wc -l | tr -d ' ')\nenv > '" + capDir + "'/$n.env\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	reg.SetCommandConfig(fixedCommandConfig(script))

	write := func(name, fm string) {
		t.Helper()
		p := filepath.Join(CrewPromptDir(), name+".md")
		if err := os.WriteFile(p, []byte("+++\n"+fm+"+++\n# "+name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("envcrew", "restart_on_crash = false\nenv = [\"CREW_TOKEN=abc\", \"POGO_AGENT_NAME=impostor\", \"POGO_AGENT_TYPE=impostor\", \"POGO_PROCESS_NAME=impostor\", \"POGO_ROLE=x\"]\n")

	a, err := reg.StartCrewAgent("envcrew")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	waitDone(t, a)
	// Edit the prompt before the crash restart: the restart must run the env
	// captured at spawn, not re-read the prompt.
	write("envcrew", "restart_on_crash = false\nenv = [\"CREW_TOKEN=edited\"]\n")
	b, err := reg.Respawn("envcrew")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)

	for _, f := range []string{"0.env", "1.env"} {
		env := readEnvCapture(t, filepath.Join(capDir, f))
		if env["CREW_TOKEN"] != "abc" {
			t.Errorf("%s: CREW_TOKEN = %q, want abc", f, env["CREW_TOKEN"])
		}
		if env["POGO_AGENT_NAME"] != "envcrew" {
			t.Errorf("%s: POGO_AGENT_NAME = %q, frontmatter must not override identity", f, env["POGO_AGENT_NAME"])
		}
		if env[AgentTypeEnv] != string(TypeCrew) || env[ProcessNameEnv] != ProcessName(TypeCrew, "envcrew") {
			t.Errorf("%s: %s = %q, %s = %q, frontmatter must not override identity",
				f, AgentTypeEnv, env[AgentTypeEnv], ProcessNameEnv, env[ProcessNameEnv])
		}
		// POGO_ROLE is not in crew identity, so only the reservation keeps the
		// prompt's value out. "Unchanged", not "absent": the test process's own
		// environment (inherited via os.Environ) may carry one.
		if env[RoleEnv] == "x" || env[RoleEnv] != os.Getenv(RoleEnv) {
			t.Errorf("%s: %s = %q, want the inherited %q; POGO_* is reserved", f, RoleEnv, env[RoleEnv], os.Getenv(RoleEnv))
		}
	}
	if !reflect.DeepEqual(a.baseEnv, []string{"CREW_TOKEN=abc"}) {
		t.Errorf("spawn baseEnv = %q, want only the non-reserved entry", a.baseEnv)
	}
	if b.baseEnv == nil || !reflect.DeepEqual(b.baseEnv, a.baseEnv) {
		t.Errorf("respawn baseEnv = %q, want the spawn-captured %q", b.baseEnv, a.baseEnv)
	}

	// A crew prompt with no env key gets no base env.
	write("plaincrew", "restart_on_crash = false\n")
	c, err := reg.StartCrewAgent("plaincrew")
	if err != nil {
		t.Fatalf("StartCrewAgent(plaincrew): %v", err)
	}
	waitDone(t, c)
	if c.baseEnv != nil {
		t.Errorf("plaincrew baseEnv = %q, want nil", c.baseEnv)
	}
}

type fixedCommandConfig string

func (c fixedCommandConfig) AgentCommand(string) string { return string(c) }
func (fixedCommandConfig) AgentProvider(string) string  { return "" }
