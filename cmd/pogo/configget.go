package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/config"
)

// configGetKeys are the keys `pogo config get` answers, each resolved exactly
// as pogod resolves it — with the same fallbacks — so an out-of-process reader
// gets the name pogod would mail rather than a second parse of the TOML.
//
// It exists for scripts/launchd/pogo-deploy.sh (drellem2/pogo#148). The nightly
// runs out of process, bounces pogod, and must still know where to send an
// alert while pogod is down, so the read cannot go over pogod's API: this
// command reads the config files in-process (config.Load) and never contacts
// the daemon. The coordinator is the name the rest of this CLI resolved at
// startup (resolveRoles), which honours a running coordinator over a renamed
// config key, as pogod does.
//
// Deliberately a short allowlist, not a general key walker: each entry is a
// name some out-of-process sender needs, and an unknown key is refused rather
// than answered with "".
var configGetKeys = map[string]func() string{
	"agents.coordinator": func() string { return agent.CoordinatorName() },
	"agents.escalation_box": func() string {
		return config.Load().Agents.EscalationBoxName()
	},
}

func configGetKeyNames() []string {
	names := make([]string, 0, len(configGetKeys))
	for k := range configGetKeys {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// newConfigCmd builds `pogo config`, currently only `pogo config get`.
func newConfigCmd(jsonOutput *bool) *cobra.Command {
	cmdConfig := &cobra.Command{
		Use:   "config",
		Short: "Read resolved configuration values",
	}
	cmdGet := &cobra.Command{
		Use:   "get <key>",
		Short: "Print a resolved configuration value (works while pogod is down)",
		Long: `Print the value pogod would use for a configuration key, with the same
defaults and fallbacks. Reads the config files in this process and never
contacts pogod, so it answers while the daemon is down.

Keys: ` + strings.Join(configGetKeyNames(), ", "),
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			get, ok := configGetKeys[args[0]]
			if !ok {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("unknown key %q (known: %s)",
					args[0], strings.Join(configGetKeyNames(), ", ")), cli.ExitError)
			}
			v := get()
			if *jsonOutput {
				cli.PrintJSON(map[string]string{"key": args[0], "value": v})
				return
			}
			fmt.Println(v)
		},
	}
	cmdConfig.AddCommand(cmdGet)
	return cmdConfig
}
