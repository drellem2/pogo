package agent

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestSpawnInjectsExactlyWhatTheCatalogueReports is the property the whole
// change rests on: `pogo agent env` reads the same list the spawn path assigns
// from, so a variable cannot be injected without being reported and cannot be
// reported without being injected.
//
// Written as a set comparison rather than a list of names, because a test that
// enumerates the names is a THIRD copy of the list and would drift with either.
func TestSpawnInjectsExactlyWhatTheCatalogueReports(t *testing.T) {
	spawned := envAssignments(agentIdentityEnv(
		"pfbaf", TypePolecat, "pogo-cat-pfbaf", "/prompts/pfbaf.md", "/receipts/pfbaf.log"))

	injected := map[string]bool{}
	for _, a := range spawned {
		name, _, ok := strings.Cut(a, "=")
		if !ok {
			t.Fatalf("assignment %q is not NAME=VALUE", a)
		}
		injected[name] = true
	}

	reported := map[string]bool{}
	for _, v := range PolecatEnv(WorkerBudget{Cores: 3, HostCores: 10, Basis: "test"}) {
		reported[v.Name] = true
	}

	for name := range injected {
		if !reported[name] {
			t.Errorf("%s is injected into every agent and reported by nothing — "+
				"this is the shape mg-fbaf was filed about", name)
		}
	}
	// The reverse direction is not a strict equality: PolecatEnv also carries
	// the two budget variables and POGO_ROLE, which polecatSpawnEnv adds rather
	// than agentIdentityEnv. Those are asserted below.
	for _, name := range []string{WorkerCoresEnv, HostCoresEnv, RoleEnv} {
		if !reported[name] {
			t.Errorf("PolecatEnv omits %s, which polecatSpawnEnv injects", name)
		}
	}
}

// TestPolecatEnvReportsTheInjectionOrder. The order is load-bearing — exec.Cmd
// keeps the LAST assignment of a duplicated key — so a report that scrambled it
// would answer the question wrongly for the two cases anybody asks about: can a
// dispatcher override the budget (yes) and can it override the role (no).
func TestPolecatEnvReportsTheInjectionOrder(t *testing.T) {
	vars := PolecatEnv(WorkerBudget{Cores: 3, HostCores: 10, Basis: "test"})
	pos := map[string]int{}
	for i, v := range vars {
		pos[v.Name] = i
	}
	if pos[WorkerCoresEnv] > pos[RoleEnv] {
		t.Errorf("the budget must be reported BEFORE POGO_ROLE: --env overrides the budget and not the role")
	}
	if pos[AgentNameEnv] > pos[WorkerCoresEnv] {
		t.Errorf("identity variables are injected before the budget; report order must match")
	}
	if vars[len(vars)-1].Name != RoleEnv {
		t.Errorf("POGO_ROLE is injected last; got %s last", vars[len(vars)-1].Name)
	}
}

// TestBudgetValuesFlowFromTheBudget, and an underived budget reports the names
// without inventing numbers. A daemon that cannot be reached must not turn the
// whole enumeration off: the names and consumers were the half that was
// believed absent, and they are knowable without it.
func TestBudgetValuesFlowFromTheBudget(t *testing.T) {
	known := byName(PolecatEnv(WorkerBudget{Cores: 3, HostCores: 10, Basis: "test"}))
	if got := known[WorkerCoresEnv]; got.Value != "3" || !got.Set {
		t.Errorf("POGO_WORKER_CORES value = %q set=%v, want 3/true", got.Value, got.Set)
	}
	if got := known[HostCoresEnv]; got.Value != "10" {
		t.Errorf("POGO_HOST_CORES value = %q, want 10", got.Value)
	}

	unknown := byName(PolecatEnv(WorkerBudget{}))
	if got := unknown[WorkerCoresEnv]; got.Set || got.Value != "" {
		t.Errorf("an underived budget must not render a value; got %q set=%v", got.Value, got.Set)
	}
	if unknown[WorkerCoresEnv].Why == "" || unknown[AgentNameEnv].Why == "" {
		t.Errorf("an underived budget suppressed the purposes, which do not depend on it")
	}
}

// TestExistenceAndCaveatAreSeparateFields. mg-fbaf's sharpest instance was one
// sentence carrying "it exists / it is delivered / nothing enforces it", read by
// two people who took the emphatic third and dropped the first two. The fields
// are held apart so a renderer cannot glue them back together by accident.
func TestExistenceAndCaveatAreSeparateFields(t *testing.T) {
	cores := byName(PolecatEnv(WorkerBudget{Cores: 3, HostCores: 10, Basis: "test"}))[WorkerCoresEnv]
	if cores.Note == "" {
		t.Fatalf("POGO_WORKER_CORES lost the note that nothing enforces it")
	}
	if strings.Contains(strings.ToLower(cores.Why), "nothing enforces") {
		t.Errorf("the caveat leaked back into Why, which is the sentence shape the ticket is about: %q", cores.Why)
	}
	if !strings.Contains(strings.ToLower(cores.Note), "enforce") {
		t.Errorf("Note no longer says what the variable does not buy: %q", cores.Note)
	}
}

// TestPromptConsumersFindsTheConsumerNobodySaw is the positive pin on instance
// 4 of mg-fbaf: the claim routed as a ticket was "nothing consults
// POGO_WORKER_CORES", and the polecat prompt templates consult it. If this ever
// goes empty legitimately — templates dropped the variable — the failure is
// still the right one to see, because the budget would then be delivered to
// nobody.
func TestPromptConsumersFindsTheConsumerNobodySaw(t *testing.T) {
	got := PromptConsumers(WorkerCoresEnv)
	if len(got) == 0 {
		t.Fatalf("no shipped prompt references $%s — either the templates dropped it "+
			"or the search is broken; both are worth stopping for", WorkerCoresEnv)
	}
	found := false
	for _, f := range got {
		if f == "templates/polecat.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("templates/polecat.md is the consumer mg-fbaf could not see; got %v", got)
	}
}

// TestPromptConsumersCanReturnEmpty proves the search is capable of the negative
// it would report — an assertion satisfied by the defect it exists to catch is
// the failure mode this repo has hit before (mg-c18d, mg-dae3). A consumer
// scanner that matched everything would pass the test above while telling every
// reader that every variable has a consumer.
func TestPromptConsumersCanReturnEmpty(t *testing.T) {
	if got := PromptConsumers("POGO_NO_SUCH_VARIABLE_ANYWHERE"); len(got) != 0 {
		t.Errorf("a variable no prompt mentions reported consumers %v — the search matches too much", got)
	}
}

// TestPromptConsumersReadsTheCorpusNotAList drives the scanner over a fixture so
// the mechanics are pinned independently of what the shipped templates happen to
// say today: it matches $NAME and ${NAME}, it does not match a longer name that
// merely starts with the same characters, and it reports each file once.
func TestPromptConsumersReadsTheCorpusNotAList(t *testing.T) {
	fsys := fstest.MapFS{
		"a.md":            {Data: []byte("use $POGO_WORKER_CORES twice: $POGO_WORKER_CORES\n")},
		"sub/b.md":        {Data: []byte("braced ${POGO_WORKER_CORES}\n")},
		"c.md":            {Data: []byte("a longer name $POGO_WORKER_CORES_EXTRA\n")},
		"d.md":            {Data: []byte("no reference at all\n")},
		"notes.txt":       {Data: []byte("$POGO_WORKER_CORES in a txt file\n")},
		"binary.tar.gz":   {Data: []byte("$POGO_WORKER_CORES")},
		"sub/deep/e.md":   {Data: []byte("$POGO_HOST_CORES only\n")},
		"sub/deep/f.json": {Data: []byte("$POGO_WORKER_CORES")},
	}
	got := promptConsumersIn(fsys, "POGO_WORKER_CORES")
	want := []string{"a.md", "notes.txt", "sub/b.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("consumers = %v, want %v", got, want)
	}
}

// TestNoEnvLiteralsLeftInTheSpawnPath. The catalogue is only a single source of
// truth while the spawn path assigns through it; a literal added back beside it
// would reinstate exactly the drift this replaces, and would do so silently
// because both halves would still compile and both would still be true of
// SOMETHING.
func TestNoEnvLiteralsLeftInTheSpawnPath(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read agent.go: %v", err)
	}
	for _, name := range []string{AgentNameEnv, AgentTypeEnv, ProcessNameEnv, AgentPromptEnv, SubmitReceiptEnv} {
		if lit := "\"" + name + "=\""; strings.Contains(string(src), lit) {
			t.Errorf("agent.go assigns %s from a literal — assign it through agentIdentityEnv "+
				"so `pogo agent env` cannot disagree with what is injected", name)
		}
	}
}

func byName(vars []InjectedEnv) map[string]InjectedEnv {
	out := make(map[string]InjectedEnv, len(vars))
	for _, v := range vars {
		out[v.Name] = v
	}
	return out
}
