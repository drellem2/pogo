package main

// check-prompts: the prompt-vs-CLI-surface detector (mg-21b1).
//
// Newest of the check-* family, and the only one that judges TEXT rather than
// fleet state. Same posture as its siblings: it reports and never acts.
//
// It exists because three shipped prompts in one night each instructed an
// invocation the CLI rejects, and all three were found by someone RUNNING the
// command — never by reading the prompt. See internal/promptcli's package
// comment for the three, and for the constraint that shapes the whole design:
// the extracted commands are NEVER executed. `mg archive --days=0` and
// `pogo agent stop <name>` are in the corpus this reads.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/promptcli"
	"github.com/drellem2/pogo/internal/providers"
)

// PromptCLIOmissions is the omission registry: the questions `--help` cannot
// answer, each backed by the code that does the refusing.
//
// One entry. `pogo agent spawn-polecat --template` is conditionally required —
// cobra cannot express that and prints nothing about it — so the rule calls
// agent.TemplateForType, the same closed map handleSpawnPolecat consults. If
// the map grows a `task` entry tomorrow, this rule stops firing on its own
// rather than becoming a second copy that drifts.
func PromptCLIOmissions() []promptcli.OmissionRule {
	return []promptcli.OmissionRule{{
		Path: []string{"pogo", "agent", "spawn-polecat"},
		Flag: "template",
		AcceptedWithout: func(scope string) bool {
			_, ok := agent.TemplateForType(scope)
			return ok
		},
		Why: "the type→template map is closed (internal/agent/templateroute.go): an unmapped " +
			"type selects no template and pogod refuses the spawn with a 409. Mapped types are " +
			strings.Join(agent.MappedTypes(), ", "),
	}}
}

// PromptCLIValues is the value registry: the flags whose accepted VALUES the
// check can answer for, each backed by the thing that does the refusing (mg-9324).
//
// Two entries, and the shortness is the finding rather than a placeholder. Of
// the 153 value-bearing flags across `mg` and `pogo`, four declare a parseable
// enumeration anywhere machine-readable, and one of those four is stale in the
// direction that invents false findings — see promptcli.ParseFlagSpecs for the
// full measurement and why `--provider`'s `(claude, codex, pi)` cannot be
// believed. Everything not listed here is UNCHECKED and is reported as such by
// the coverage census.
//
// The registry does NOT restate any value list. Each entry points at the source:
//
//   - `mg list --status` reads the enumeration out of the flag's own help text,
//     which macguffin GENERATES from `listStatusValues` — the same slice
//     `isValidListStatus` rejects against (cmd/mg/list.go:32-41,183). The
//     projection cannot drift from the validator, so parsing it is sound.
//   - `pogo agent spawn-polecat --provider` calls providers.Resolve, the function
//     that decides. Its help text says `(claude, codex, pi)` and MISSES `cursor`,
//     which Resolve accepts — so the callable source is not merely preferable
//     here, it is the only correct one.
//
// `mg edit --priority` / `mg new --priority` are deliberately absent. Both
// declare `low, medium, high` and both validate it, but as two independent
// hand-written copies (cmd/mg/edit.go:266-270, new.go:211) with the help string
// a third. There is no projection to read, so registering it would make this
// file the fourth copy — exactly what the doc comment forbids.
func PromptCLIValues() []promptcli.ValueRule {
	return []promptcli.ValueRule{{
		Path:     []string{"mg", "list"},
		Flag:     "status",
		FromHelp: true,
		Why: "macguffin renders this flag's help text from listStatusValues, the same slice " +
			"isValidListStatus rejects against, so the two cannot drift",
	}, {
		Path: []string{"pogo", "agent", "spawn-polecat"},
		Flag: "provider",
		Accepts: func(v string) bool {
			_, ok := providers.Resolve(v)
			return ok
		},
		Legal: func() []string {
			var ids []string
			for _, p := range providers.All() {
				ids = append(ids, p.ID)
			}
			return ids
		},
		Why: "providers.Resolve is the function that decides, and it accepts one more id " +
			"than the flag's own help text lists",
	}}
}

// PromptCLISurfaces discovers the accept-surface of every CLI the prompts
// instruct.
//
// pogoBin should be the binary built from the revision under check, NOT the one
// on PATH. While mg-21b1 was being written the installed `pogo` had no
// `check-intake` and the source three commits back did; a check run against
// PATH would have reported mayor.md's correct instruction as a defect. Passing
// "" falls back to this process's own executable, which is the right answer for
// the command below and the wrong one for a test of uninstalled source.
//
// A missing `mg` is reported as a missing surface rather than an error: `mg` is
// a separate tool that may not be installed, and half a check is worth more
// than none. The caller decides whether to treat that as fatal.
func PromptCLISurfaces(ctx context.Context, pogoBin string) (map[string]*promptcli.Surface, []string, error) {
	if pogoBin == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, nil, fmt.Errorf("locating this executable: %w", err)
		}
		pogoBin = self
	}
	surfaces := map[string]*promptcli.Surface{}
	var missing []string

	pogoSurface, err := promptcli.DiscoverBinary(ctx, pogoBin, "pogo")
	if err != nil {
		return nil, nil, fmt.Errorf("discovering the pogo surface from %s: %w", pogoBin, err)
	}
	surfaces["pogo"] = pogoSurface

	mgBin, err := exec.LookPath("mg")
	if err != nil {
		missing = append(missing, "mg")
	} else {
		mgSurface, err := promptcli.DiscoverBinary(ctx, mgBin, "mg")
		if err != nil {
			return nil, nil, fmt.Errorf("discovering the mg surface from %s: %w", mgBin, err)
		}
		surfaces["mg"] = mgSurface
	}
	return surfaces, missing, nil
}

func newCheckPromptsCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "check-prompts",
		Short: "Report shipped prompts that instruct an invocation the CLI would reject (never runs them)",
		Long: `Check every ` + "`mg …`" + ` and ` + "`pogo …`" + ` invocation the shipped prompt corpus
instructs against the real accept-surface of those tools.

WHY. Three shipped prompts in one night each told their reader to run something
the CLI rejects, and all three were found by someone RUNNING the command, never
by reading the prompt:

  mg-159a  mayor.md said to omit --template for a bare ` + "`task`" + ` item. The
           type->template map is closed Go and refuses with a 409, and ` + "`task`" + ` is
           the DEFAULT type -- so this was wrong about the ordinary dispatch.
  mg-4bb9  three prompts said ` + "`mg edit`" + ` has no append subcommand and to mail the
           note instead. --append-body and --append-body-file exist, and
           ` + "`mg edit --help`" + ` OPENS by recommending the second one.
  mg-d8ea  pm-template.md said ` + "`mg spend --by item --tag=<tag>`" + `. There is no --tag
           on mg spend; the working form is --by tag:<tag>.

Each failed quietly at the moment of use, and mg-4bb9 shows why reading cannot
catch these: a confident false claim FORECLOSES the --help that would correct
it. Silence makes a reader look; a wrong answer stops them.

  mg-9324  pm-template.md said ` + "`mg list --status=closed`" + `. ` + "`--status`" + ` is real and
           ` + "`closed`" + ` is a plausible string, so the flag-surface check passed it;
           ` + "`done`" + ` is the accepted spelling.

IT NEVER RUNS THE EXTRACTED COMMANDS. The corpus contains
` + "`mg archive --days=0`" + `, ` + "`mg reopen <id>`" + ` and ` + "`pogo agent stop <name>`" + `; a control
that executed its fixtures would destroy the store it runs in. The only process
it starts is ` + "`<binary> <path...> --help`" + `, which cobra answers before it reaches
any command's body. It verifies that a flag EXISTS, never that it works.

WHAT IT CHECKS, in five shapes:

  unknown-flag        a flag the command does not accept
  unknown-subcommand  a subcommand a command group does not have
  false-absence       the prompt says something does not exist, and it does
  bad-omission        the prompt says to leave off a flag the CLI refuses to
                      default (registry-backed; see PromptCLIOmissions)
  unknown-flag-value  the flag exists and the VALUE is refused, e.g.
                      ` + "`mg list --status=closed`" + ` where the spelling is ` + "`done`" + `
                      (registry-backed; see PromptCLIValues)

FLAG VALUES ARE MOSTLY UNCHECKABLE, and the report says so every run. Legal
values are not declared anywhere machine-readable: of the 153 value-bearing
flags across the two tools, FOUR spell out a parseable enumeration, and one of
those four is already stale in the direction that invents false findings
(` + "`--provider`" + ` lists ` + "`claude, codex, pi`" + ` and omits ` + "`cursor`" + `, which
providers.Resolve accepts). cobra has nothing better to offer: ValidArgs
describes positionals, and ` + "`RegisterFlagCompletionFunc`" + ` is called zero times
in either tool, so ` + "`__complete`" + ` answers every flag with "ask the filesystem".

So a value is judged only where a registry entry names the code that does the
refusing, and every run prints a census of the value-bearing flags it could NOT
judge. Read that census. A value-checker blind to most values is worse than
none — it converts "unchecked" into "checked and fine".

WHAT IT DOES NOT CHECK, deliberately:

  git / gh / jq       not our tools and not our surface to pin.
  bare flag mentions  a flag named with no command around it has no command to
                      be checked against.
  workflows           every token can be legal and the workflow still have no
                      mechanism behind it (mg-d8ea: no work item has a closed-at
                      field, so no spelling of ` + "`mg list`" + ` can filter on one).
                      Out of scope here and not detectable from a help page.

A correction that QUOTES the claim it withdraws is not a defect. Mark it with
` + "`<!-- promptcli:retracted -->`" + ` in the paragraph, or write it in the past tense;
an absence-predicate that scores a careful correction worse than the sloppy
original teaches people to delete the explanation.

Exit status is 0 when the corpus is clean, 1 when anything is reported.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			surfaces, missing, err := PromptCLISurfaces(cmd.Context(), "")
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			checker := &promptcli.Checker{
				Surfaces:  surfaces,
				Omissions: PromptCLIOmissions(),
				Values:    PromptCLIValues(),
			}
			rep, err := promptcli.CheckFS(checker, agent.DefaultPromptsFS())
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}

			if *jsonOutput {
				cli.PrintJSON(map[string]any{
					"files_checked":    rep.Files,
					"surfaces_checked": surfaceNames(surfaces),
					"surfaces_missing": missing,
					"findings":         rep.Findings,
					"values_checked":   rep.Coverage.Checked,
					"values_unchecked": rep.Coverage.Unchecked,
				})
			} else {
				for _, f := range rep.Findings {
					fmt.Println(f)
					if f.Evidence != "" {
						fmt.Printf("    %s\n", strings.TrimSpace(f.Evidence))
					}
				}
				if len(rep.Findings) == 0 {
					fmt.Printf("clean: %d prompt files, surfaces %s\n",
						rep.Files, strings.Join(surfaceNames(surfaces), " + "))
				} else {
					fmt.Printf("\n%d finding(s) across %d prompt files\n", len(rep.Findings), rep.Files)
				}
				// The census, always, clean or not. A value-checker that reports
				// only its findings reads as though it judged every value it saw;
				// this is the half that says what it could not judge.
				cov := rep.Coverage
				fmt.Printf("\nflag values: %d checkable, %d NOT CHECKED (no rule names a source for the legal set)\n",
					len(cov.Checked), len(cov.Unchecked))
				for _, s := range cov.Checked {
					fmt.Printf("    checked      %s\n", s)
				}
				for _, s := range cov.Unchecked {
					fmt.Printf("    unchecked    %s\n", s)
				}
				for _, m := range missing {
					// Not an all-clear. Say which half of the corpus went
					// unjudged rather than letting a clean report imply it was.
					fmt.Fprintf(os.Stderr,
						"warning: %s is not on PATH, so no `%s …` invocation was checked\n", m, m)
				}
			}
			if len(rep.Findings) > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
}

func surfaceNames(s map[string]*promptcli.Surface) []string {
	out := make([]string, 0, len(s))
	for _, r := range promptcli.Roots {
		if _, ok := s[r]; ok {
			out = append(out, r)
		}
	}
	return out
}
