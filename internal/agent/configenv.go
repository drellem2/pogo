package agent

import (
	"strings"
)

// AgentNamePlaceholder is the one token expanded in a configured env value: it
// becomes the name of the agent being spawned. It is what lets one line serve a
// whole agent type when each agent needs its own value — for example
// `[agents.crew] env = ["SOME_IDENTITY=${POGO_AGENT_NAME}"]`. Nothing else in a
// value is expanded: no other variables, no shell syntax.
const AgentNamePlaceholder = "${" + AgentNameEnv + "}"

// AgentEnvConfig is the optional half of AgentCommandConfig that supplies the
// configured environment for an agent type ([agents] env, then
// [agents.<type>] env). It is a separate interface, asserted at spawn, so that
// existing AgentCommandConfig implementations need no new method; one that does
// not implement it contributes no environment. *config.AgentsConfig implements
// it.
type AgentEnvConfig interface {
	AgentEnv(agentType string) []string
}

// configEnvLocked returns the configured environment for one spawn of the given
// type and name, with AgentNamePlaceholder expanded. nil when the registry has
// no config or its config supplies no environment. Caller holds r.mu.
func (r *Registry) configEnvLocked(typ AgentType, name string) []string {
	ec, ok := r.cmdConfig.(AgentEnvConfig)
	if !ok {
		return nil
	}
	entries := ec.AgentEnv(string(typ))
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = strings.ReplaceAll(e, AgentNamePlaceholder, name)
	}
	return out
}

// processEnv returns the layers pogo puts on top of pogod's own environment
// for a spawned agent. Spawn and the restart path both build them here, so a
// restarted agent gets the same environment as the one it replaces.
//
// It deliberately does NOT include os.Environ(). Callers write
// `cmd.Env = append(os.Environ(), processEnv(...)...)`, so that the root is
// visible at the call site to the gitceiling inheritance guard
// (internal/gitceiling, TestSubprocessEnvironmentsInheritTheCeiling): an
// environment that does not visibly root in os.Environ() is flagged, because it
// could drop GIT_CEILING_DIRECTORIES.
//
// The ORDER is the contract, because exec.Cmd keeps the last assignment of a
// duplicated key:
//
//  1. pogod's own environment, inherited (the caller's os.Environ()).
//  2. configEnv: [agents] env, then [agents.<type>] env. Host configuration, so
//     it is the lowest of pogo's layers.
//  3. identity: the catalogue variables (agentIdentityEnv). After the config so
//     that config cannot rename an agent or point it at another mailbox.
//  4. spawnEnv: the spawn's own SpawnRequest.Env. For a polecat that is the
//     core budget, then the dispatcher's --env, then POGO_ROLE last
//     (polecatSpawnEnv). After the config so that an explicit --env still wins,
//     as it did before config env existed.
func processEnv(configEnv, identity, spawnEnv []string) []string {
	env := make([]string, 0, len(configEnv)+len(identity)+len(spawnEnv))
	env = append(env, configEnv...)
	env = append(env, identity...)
	return append(env, spawnEnv...)
}

// ConfiguredEnv renders configured env entries as catalogue rows for
// `pogo agent env`, so that a variable config.toml injects is reported like any
// other (mg-fbaf: an environment that exists and is believed absent). Values
// are shown as configured; AgentNamePlaceholder is left in place because a
// catalogue view has no agent name to put there. Entries that are not
// KEY=VALUE are left out, as the spawn path leaves them out.
func ConfiguredEnv(entries []string) []InjectedEnv {
	var out []InjectedEnv
	for _, e := range entries {
		name, value, ok := strings.Cut(e, "=")
		if !ok || name == "" {
			continue
		}
		row := InjectedEnv{
			Name:  name,
			Value: value,
			Set:   true,
			Why:   "set by this host's config.toml ([agents] env, then [agents.<type>] env)",
			Note:  "host configuration, not a pogo variable: every variable listed after it overrides it, and pogod applies the copy of config.toml it read at startup",
		}
		if strings.Contains(value, AgentNamePlaceholder) {
			row.When = AgentNamePlaceholder + " becomes the spawned agent's own name"
		}
		out = append(out, row)
	}
	return out
}
