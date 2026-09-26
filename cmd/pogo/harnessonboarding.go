package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/providers"
)

// harnessOnboarding is what `pogo install` and `pogo doctor --check` both say
// about one configured agent harness's first-run gates (drellem2/pogo#173):
// the permission posture pogo runs it in, and whether it has a login.
type harnessOnboarding struct {
	Provider         string `json:"provider"`
	Binary           string `json:"binary"`
	BinaryPath       string `json:"binaryPath,omitempty"`
	PermissionNotice string `json:"permissionNotice,omitempty"`
	// Login is "logged_in", "not_logged_in", "unknown", or "" when the binary
	// is not on PATH or the provider declares no login probe.
	Login       string `json:"login,omitempty"`
	LoginDetail string `json:"loginDetail,omitempty"`
}

// harnessLoginProbeTimeout bounds each login probe `pogo install` and
// `pogo doctor --check` run. Neither may hang on a wedged harness CLI.
const harnessLoginProbeTimeout = 15 * time.Second

// configuredHarnessOnboarding reports every distinct harness the crew and
// polecat types are configured to use — the same enumeration doctor's
// "<binary> in PATH" rows walk.
func configuredHarnessOnboarding() []harnessOnboarding {
	agentsCfg := config.Load().Agents
	seen := map[string]bool{}
	var out []harnessOnboarding
	for _, agentType := range []string{"crew", "polecat"} {
		p, _ := providers.Resolve(agentsCfg.AgentProvider(agentType))
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, harnessOnboardingFor(p, exec.LookPath))
	}
	return out
}

func harnessOnboardingFor(p *agent.Provider, lookPath func(string) (string, error)) harnessOnboarding {
	h := harnessOnboarding{Provider: p.ID, Binary: p.Binary, PermissionNotice: p.PermissionNotice}
	path, err := lookPath(p.Binary)
	if err != nil {
		return h
	}
	h.BinaryPath = path
	if p.AuthPreflight == nil {
		return h
	}
	ctx, cancel := context.WithTimeout(context.Background(), harnessLoginProbeTimeout)
	defer cancel()
	r := p.AuthPreflight(ctx, path)
	h.Login, h.LoginDetail = r.State.String(), r.Detail
	return h
}

// printHarnessOnboarding is `pogo install`'s human-readable form of the rows.
func printHarnessOnboarding(w io.Writer, hs []harnessOnboarding) {
	for _, h := range hs {
		if h.PermissionNotice != "" {
			fmt.Fprintf(w, "  ℹ %s\n", h.PermissionNotice)
		}
		switch h.Login {
		case "logged_in":
			fmt.Fprintf(w, "  ✓ %s is logged in\n", h.Binary)
		case "not_logged_in":
			fmt.Fprintf(w, "  ✗ %s is NOT logged in — run `%s` once in a terminal and log in.\n"+
				"    Until you do, pogod will not auto-start the crew: an agent would stall at the login screen.\n",
				h.Binary, h.Binary)
		case "unknown":
			fmt.Fprintf(w, "  ⚠ could not determine whether %s is logged in (%s)\n", h.Binary, h.LoginDetail)
		}
	}
}
