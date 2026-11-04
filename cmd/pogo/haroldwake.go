package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/haroldwake"
)

// newHookHaroldWakeCmd is `pogo hook harold-wake`, the command harold's daemon
// runs (through ~/.pogo/bin/harold-wake) when a Discord ping lands. The logic
// is in internal/haroldwake; pogod neither runs nor reads it.
func newHookHaroldWakeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "harold-wake",
		Short: "harold's [inbox] notify hook: mail the agent a Discord ping is for",
		Long: `Mail the pogo agent behind a harold identity that it has new Discord pings.

harold runs this as its [inbox] notify command, through the wrapper that
'pogo install' writes to ~/.pogo/bin/harold-wake. It reads harold's JSON object
on stdin and $HAROLD_IDENTITY, maps the identity to an agent (by name, or by a
line in ~/.pogo/harold-wake.conf), and sends

  mg mail send <agent> --from=harold --subject="Discord: <n> new pings; call discord_poll"

Only the count crosses: the message text was written by someone outside the
fleet and never reaches the mail. An identity that maps to no coordinator or
crew agent wakes nobody and exits 0. See docs/CONFIGURATION.md, "Waking an agent
on Discord activity".`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(haroldwake.Run(haroldwake.Options{
				Stdin:    os.Stdin,
				Stderr:   os.Stderr,
				Identity: os.Getenv("HAROLD_IDENTITY"),
				MapPath:  haroldwake.MapPath(config.PogoHome()),
				Known:    haroldwake.KnownAgents,
				MG:       haroldwake.DefaultFinder().MG,
			}))
		},
	}
}

// installHaroldWakeShim writes ~/.pogo/bin/harold-wake pointing at the pogo
// running this install. It is inert until harold's config names it, so
// `pogo install` writes it on every host rather than asking.
func installHaroldWakeShim() (path string, changed bool, err error) {
	dir := filepath.Join(config.PogoHome(), "bin")
	path = filepath.Join(dir, haroldwake.ShimName)
	exe, err := os.Executable()
	if err != nil {
		return path, false, err
	}
	changed, err = haroldwake.InstallShim(dir, exe)
	return path, changed, err
}
