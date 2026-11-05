package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeAgentsConfig writes body as the only config.toml Load will find.
func writeAgentsConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	pogoDir := filepath.Join(dir, "pogo")
	if err := os.MkdirAll(pogoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestAgentsEnvConfigFile: [agents] env applies to every type, and
// [agents.<type>] env is appended after it, so a key set in both takes the
// per-type value (a process keeps the last assignment).
func TestAgentsEnvConfigFile(t *testing.T) {
	writeAgentsConfig(t, strings.Join([]string{
		"[agents]",
		`env = ["SHARED=1", "WHO=nobody"]`,
		"[agents.crew]",
		`env = ["WHO=${POGO_AGENT_NAME}"]`,
		"[agents.polecat]",
		`env = ["WHO=none", "OPTS=a,b"]`,
		"",
	}, "\n"))
	a := Load().Agents

	cases := map[string][]string{
		"crew":    {"SHARED=1", "WHO=nobody", "WHO=${POGO_AGENT_NAME}"},
		"polecat": {"SHARED=1", "WHO=nobody", "WHO=none", "OPTS=a,b"},
		// A type with no section of its own gets the global list only.
		"other": {"SHARED=1", "WHO=nobody"},
	}
	for typ, want := range cases {
		if got := a.AgentEnv(typ); !reflect.DeepEqual(got, want) {
			t.Errorf("AgentEnv(%q) = %q, want %q", typ, got, want)
		}
	}
	if p := a.EnvProblems(); len(p) != 0 {
		t.Errorf("EnvProblems() = %q, want none", p)
	}
}

// TestAgentsEnvUnset: no env anywhere means no environment, not an empty
// assignment.
func TestAgentsEnvUnset(t *testing.T) {
	writeAgentsConfig(t, "[agents]\ncoordinator = \"boss\"\n")
	a := Load().Agents
	if got := a.AgentEnv("crew"); got != nil {
		t.Errorf("AgentEnv(crew) = %q, want nil", got)
	}
	var nilCfg *AgentsConfig
	if got := nilCfg.AgentEnv("crew"); got != nil {
		t.Errorf("nil AgentsConfig AgentEnv = %q, want nil", got)
	}
	if got := nilCfg.EnvProblems(); got != nil {
		t.Errorf("nil AgentsConfig EnvProblems = %q, want nil", got)
	}
}

// TestAgentsEnvBadEntriesSkippedAndNamed: an entry that is not KEY=VALUE
// with a portable name is left out of AgentEnv, and EnvProblems names it with
// its section, so pogod can warn instead of dropping it silently.
func TestAgentsEnvBadEntriesSkippedAndNamed(t *testing.T) {
	writeAgentsConfig(t, strings.Join([]string{
		"[agents]",
		`env = ["GOOD=1", "NOEQUALS", "=empty", "1BAD=x", "BAD-NAME=x", "EMPTY_OK="]`,
		"[agents.polecat]",
		`env = ["ALSO BAD=x"]`,
		"",
	}, "\n"))
	a := Load().Agents

	if got, want := a.AgentEnv("crew"), []string{"GOOD=1", "EMPTY_OK="}; !reflect.DeepEqual(got, want) {
		t.Errorf("AgentEnv(crew) = %q, want %q", got, want)
	}
	problems := a.EnvProblems()
	if len(problems) != 5 {
		t.Fatalf("EnvProblems() = %q, want 5 entries", problems)
	}
	for _, want := range []string{
		`[agents] env: "NOEQUALS" has no "="`,
		`[agents] env: "=empty" has an empty name`,
		`[agents] env: "1BAD=x" has a name`,
		`[agents] env: "BAD-NAME=x" has a name`,
		`[agents.polecat] env: "ALSO BAD=x" has a name`,
	} {
		found := false
		for _, p := range problems {
			if strings.HasPrefix(p, want) && strings.HasSuffix(p, "; skipped") {
				found = true
			}
		}
		if !found {
			t.Errorf("EnvProblems() = %q, missing a line starting %q", problems, want)
		}
	}
}

// TestParseQuotedStringArray: a comma inside a quoted element belongs to the
// element. parseStringArray splits it, which is the reason env does not use it.
func TestParseQuotedStringArray(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["A=1", "B=2"]`, []string{"A=1", "B=2"}},
		{`["OPTS=a,b", 'C=3']`, []string{"OPTS=a,b", "C=3"}},
		{`["Q=say \"hi\"", "T=a\tb"]`, []string{`Q=say "hi"`, "T=a\tb"}},
		{`['LIT=C:\path\n']`, []string{`LIT=C:\path\n`}},
		{`[ BARE=1 , "X=2" ]`, []string{"BARE=1", "X=2"}},
		{`["SPACE= padded "]`, []string{"SPACE= padded "}},
		{`[]`, nil},
		{`["", "A=1",]`, []string{"A=1"}},
	}
	for _, c := range cases {
		if got := parseQuotedStringArray(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseQuotedStringArray(%s) = %q, want %q", c.in, got, c.want)
		}
	}
	// The control: the old parser does split the quoted comma.
	if got := parseStringArray(`["OPTS=a,b"]`); len(got) != 2 {
		t.Errorf("parseStringArray control = %q; expected it to split the comma (2 entries)", got)
	}
}
