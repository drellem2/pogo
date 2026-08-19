package agent

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Environment variables pogod injects into every agent process it starts.
// Named as constants because two spawn paths assign them and one report
// enumerates them, and a literal repeated in three places is a list that can
// disagree with itself.
const (
	// AgentNameEnv is the agent's own name — the string `--from=` takes and
	// the mailbox replies come back to.
	AgentNameEnv = "POGO_AGENT_NAME"
	// AgentTypeEnv is the agent's type (crew, polecat, coordinator).
	AgentTypeEnv = "POGO_AGENT_TYPE"
	// ProcessNameEnv is the display label. It is NOT set on any process, so
	// `pgrep -f` against it matches nothing even while the agent is healthy
	// (mg-710c).
	ProcessNameEnv = "POGO_PROCESS_NAME"
	// AgentPromptEnv is the path to the persona prompt file, when there is one.
	AgentPromptEnv = "POGO_AGENT_PROMPT"
	// SubmitReceiptEnv is where the harness's prompt-submission hook appends
	// delivery receipts, when the provider has a hook to install.
	SubmitReceiptEnv = "POGO_SUBMIT_RECEIPT"
	// RoleEnv is the frozen cross-tool role identifier (mg-6a24 §1.1).
	RoleEnv = "POGO_ROLE"
)

// InjectedEnv is one environment variable pogod sets on an agent process: what
// it is, when it is set, and — separately — what it does not buy.
//
// # Why this type exists
//
// mg-fbaf recorded five occasions in one night on which a mechanism that
// existed and worked was believed absent. Two of them were this variable set:
// POGO_WORKER_CORES was believed not to exist, and then, by a second reader,
// believed to have no consumer. Both were reading the line that described it:
//
//	Advisory: it reaches a worker as $POGO_WORKER_CORES and prompt prose.
//
// "and prompt prose" names the consumer. It was in the sentence. The sentence's
// emphasis is the absence of enforcement, and both readers took the emphasis
// and missed the enumeration riding inside it.
//
// So the remedy is not a better sentence, and the ticket says so explicitly:
// all five mechanisms were documented, which is the finding. What this type
// does instead is split the sentence into fields a reader cannot take only half
// of. Why says what the variable is; Note says what it does not buy. A reader
// who reads only Note still gets Name.
//
// # The half that is derived rather than asserted
//
// Consumers are NOT a field. They are computed from the shipped prompt corpus
// by PromptConsumers, because the claim that got it wrong was "nothing consults
// it" — a claim about the corpus that can only be settled by reading the
// corpus. A stated consumer list would go stale the first time a template was
// edited, and would then be the same defect wearing this ticket's fix as a
// label.
type InjectedEnv struct {
	// Name is the variable's name.
	Name string `json:"name"`
	// Value is what this spawn assigns. Empty when this is a catalogue entry
	// rather than a spawn, in which case Placeholder describes the value.
	Value string `json:"value,omitempty"`
	// Placeholder describes the value for a reader asking what a worker gets
	// without a worker in front of them.
	Placeholder string `json:"placeholder,omitempty"`
	// Set reports whether this spawn assigns the variable at all. An unset
	// variable says "nobody told me"; a variable set to an empty string does
	// not, which is why the two are distinguished rather than both rendered.
	Set bool `json:"set"`
	// Why is what the variable is for. One clause, no caveats — the caveats
	// have their own field on purpose.
	Why string `json:"why"`
	// When names the condition under which it is set, or "" for always.
	When string `json:"when,omitempty"`
	// Note is what the variable does NOT buy: unenforced, advisory, frozen.
	// Held apart from Why so that the emphatic half cannot swallow the
	// existence half, which is exactly what happened to POGO_WORKER_CORES.
	Note string `json:"note,omitempty"`
}

// envSpec is the catalogue entry behind an InjectedEnv: everything true of the
// variable regardless of which spawn is being described.
type envSpec struct {
	Name        string
	Placeholder string
	Why         string
	When        string
	Note        string
}

// agentIdentitySpecs is the ordered catalogue of identity variables. It is the
// single list: startAgentProcess and the restart path assign values THROUGH it,
// and PolecatEnv reports it, so a variable cannot be injected without appearing
// in the report and cannot be reported without being injected.
var agentIdentitySpecs = []envSpec{
	{
		Name:        AgentNameEnv,
		Placeholder: "<the agent's own name>",
		Why:         "the agent's name: what `mg mail send --from=` takes, and the mailbox replies come back to",
	},
	{
		Name:        AgentTypeEnv,
		Placeholder: "<crew|polecat|coordinator>",
		Why:         "the agent's type in pogod's registry",
	},
	{
		Name:        ProcessNameEnv,
		Placeholder: "pogo-cat-<name>",
		Why:         "the display label `pogo agent list` shows",
		Note:        "not set on any process — `pgrep -f` against it matches nothing even while the agent is healthy (mg-710c)",
	},
	{
		Name:        AgentPromptEnv,
		Placeholder: "<path to the persona prompt file>",
		Why:         "where the agent's persona prompt was written",
		When:        "the agent has a persona prompt file",
	},
	{
		Name:        SubmitReceiptEnv,
		Placeholder: "<path the submit hook appends to>",
		Why:         "where the harness's prompt-submission hook records deliveries",
		When:        "the provider has a submit hook that could be installed",
		Note:        "absent means this agent cannot prove delivery; nudges to it fall back to wait-idle",
	},
}

// workerBudgetSpecs is the catalogue for the two budget variables. Kept beside
// the identity list rather than inside it because they are injected at a
// different point in the order and only for workers.
var workerBudgetSpecs = []envSpec{
	{
		Name:        WorkerCoresEnv,
		Placeholder: "<this worker's share, in whole cores>",
		Why:         "how many of this host's cores this worker may plan to use — pass it to every `-j` / `-p` / `--jobs` your toolchain has",
		Note:        "nothing enforces it: a toolchain that ignores it takes the box, and the host gate is what notices afterwards",
	},
	{
		Name:        HostCoresEnv,
		Placeholder: "<this host's core count>",
		Why:         "the denominator, so a worker can tell 3-of-10 from 3-of-4",
	},
}

// roleSpec is the catalogue entry for POGO_ROLE.
var roleSpec = envSpec{
	Name:        RoleEnv,
	Placeholder: string(TypePolecat),
	Why:         "the frozen cross-tool role identifier (mg-6a24 §1.1)",
	Note:        "injected last on purpose, so a dispatcher's --env cannot override it",
}

// entry renders a spec against a concrete value.
func (s envSpec) entry(value string) InjectedEnv {
	return InjectedEnv{
		Name:        s.Name,
		Value:       value,
		Placeholder: s.Placeholder,
		Set:         value != "",
		Why:         s.Why,
		When:        s.When,
		Note:        s.Note,
	}
}

// agentIdentityEnv returns the identity variables for one concrete spawn, in
// injection order. Entries whose value is empty are reported unset and are
// skipped by envAssignments — an unset variable and one assigned the empty
// string are different facts to the process receiving them.
func agentIdentityEnv(name string, typ AgentType, procName, promptFile, receiptFile string) []InjectedEnv {
	values := map[string]string{
		AgentNameEnv:     name,
		AgentTypeEnv:     string(typ),
		ProcessNameEnv:   procName,
		AgentPromptEnv:   promptFile,
		SubmitReceiptEnv: receiptFile,
	}
	out := make([]InjectedEnv, 0, len(agentIdentitySpecs))
	for _, s := range agentIdentitySpecs {
		out = append(out, s.entry(values[s.Name]))
	}
	return out
}

// envAssignments renders the set entries as NAME=VALUE, in order.
func envAssignments(vars []InjectedEnv) []string {
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		if !v.Set {
			continue
		}
		out = append(out, v.Name+"="+v.Value)
	}
	return out
}

// PolecatEnv reports the full set of variables pogod injects into a worker, in
// the order they are injected, with the budget's values filled in from the
// budget this host would hand the next worker.
//
// The ORDER is reported because it is load-bearing: exec.Cmd keeps the last
// assignment of a duplicated key, so a dispatcher's `--env POGO_WORKER_CORES=8`
// beats the static division while `POGO_ROLE` — injected after it — cannot be
// overridden at all (see polecatSpawnEnv).
//
// What this does NOT cover, stated here because an enumeration that is silent
// about its own edges is the thing this exists to stop: the worker also
// inherits pogod's entire environment (os.Environ()), and a dispatcher may add
// anything it likes with --env. Those are open sets and no list can close them.
func PolecatEnv(budget WorkerBudget) []InjectedEnv {
	out := agentIdentityEnv("", TypePolecat, "", "", "")
	// The catalogue view knows the type even though it knows no name.
	for i := range out {
		if out[i].Name == AgentTypeEnv {
			out[i] = agentIdentitySpecs[i].entry(string(TypePolecat))
		}
	}

	values := map[string]string{}
	if budget.Known() {
		for _, assignment := range budget.Env() {
			if name, value, ok := strings.Cut(assignment, "="); ok {
				values[name] = value
			}
		}
	}
	for _, s := range workerBudgetSpecs {
		e := s.entry(values[s.Name])
		if !e.Set && s.Name == WorkerCoresEnv && budget.Basis != "" {
			e.When = "a budget could be derived — " + budget.Basis
		}
		out = append(out, e)
	}
	return append(out, roleSpec.entry(string(TypePolecat)))
}

// envRefPattern matches a shell reference to a variable: $NAME or ${NAME}.
var envRefPattern = regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)\}?`)

// PromptConsumers reports which shipped prompt files reference $name, sorted.
//
// Derived, never stated. The claim that cost mg-fbaf its fourth instance was
// "nothing consults POGO_WORKER_CORES", asserted about a corpus that contained
// six consumers of it. This reads the corpus. It is the binary's own embed —
// the same bytes the installer writes — so it answers about what this pogod
// would ship rather than about whatever is on disk.
//
// Bounds, because a negative from this function is evidence only over what it
// searched: it covers the SHIPPED prompt corpus and nothing else. A consumer in
// Go code, in a user-edited prompt under ~/.pogo/agents, or in another repo's
// tooling is outside it and an empty result does not speak to those.
func PromptConsumers(name string) []string {
	return promptConsumersIn(DefaultPromptsFS(), name)
}

func promptConsumersIn(fsys fs.FS, name string) []string {
	var hits []string
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ext := path.Ext(p); ext != ".md" && ext != ".txt" {
			return nil
		}
		b, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return nil
		}
		for _, m := range envRefPattern.FindAllStringSubmatch(string(b), -1) {
			if m[1] == name {
				hits = append(hits, p)
				return nil
			}
		}
		return nil
	})
	sort.Strings(hits)
	return hits
}
