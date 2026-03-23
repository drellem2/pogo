// Package completion provides shell completion generation for pogo CLI tools.
package completion

import (
	"os"

	"github.com/spf13/cobra"
)

// AddCommand adds a "completion" subcommand to the given root command that
// generates shell completion scripts for bash, zsh, and fish.
func AddCommand(rootCmd *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Generate shell completion script",
		Long: `Generate a shell completion script for ` + rootCmd.Name() + `.

To load completions:

Bash:
  $ source <(` + rootCmd.Name() + ` completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ ` + rootCmd.Name() + ` completion bash > /etc/bash_completion.d/` + rootCmd.Name() + `
  # macOS:
  $ ` + rootCmd.Name() + ` completion bash > $(brew --prefix)/etc/bash_completion.d/` + rootCmd.Name() + `

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ ` + rootCmd.Name() + ` completion zsh > "${fpath[1]}/_` + rootCmd.Name() + `"

  # You will need to start a new shell for this setup to take effect.

Fish:
  $ ` + rootCmd.Name() + ` completion fish | source

  # To load completions for each session, execute once:
  $ ` + rootCmd.Name() + ` completion fish > ~/.config/fish/completions/` + rootCmd.Name() + `.fish
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			}
			return nil
		},
	}
	rootCmd.AddCommand(cmd)
}
