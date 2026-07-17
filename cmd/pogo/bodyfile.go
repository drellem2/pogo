package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// bodyFromFlags resolves the --body / --body-file pair on
// 'pogo agent spawn-polecat'. It is the pogo half of mg-7850's fix: mg landed
// --body-file on 'mg mail send' / 'mg new' / 'mg edit' (see
// macguffin cmd/mg/bodyfile.go, the precedent this mirrors deliberately —
// two binaries with the same hazard must not grow two spellings of the cure).
//
// WHY --body-file EXISTS, AND WHY IT MATTERS MORE HERE THAN IN mg. The shell
// expands `backticks`, $VAR and $(cmd) inside --body="..." BEFORE pogo is
// executed, so main() receives an already-mangled string and spawns a polecat
// on a dispatch body it silently corrupted. A mangled mail is embarrassing and
// gets resent; a mangled dispatch body is a requirement that never reaches the
// polecat at all, indistinguishable from one that was never written. The
// dispatch body is the durable channel — mg-ea3e merged without its required
// guard because the requirement lived only in mail, while mg-8b48 shipped its
// requirement because the body carried it and the clarifying mail lost the
// race by 44 seconds. Same pipeline, opposite channel, opposite outcome: the
// body is where requirements actually bind.
//
// pogo CANNOT detect the corruption after the fact. The shell expands before
// pogo is executed, so these two are byte-identical at the only point the
// process can observe:
//
//	--body="is missing from , and  cannot flag"   <- the shell ate three backticked terms
//	--body="is missing from , and  cannot flag"   <- someone typed exactly that
//
// Any check here would be a heuristic guessing at prose — a check that cannot
// reliably fail, which is the disease, committed while fixing the disease. So
// the fix is the incentive gradient, not a detector. Before, the safe path cost
// more keystrokes AND more knowledge than the dangerous one:
//
//	--body="..."            dangerous, short, natural — what you reach for
//	--body="$(cat file)"    safe, longer, requires knowing the trick
//	--body-file ./task.md   safe AND short — no shell in the path at all
//
// --body-file makes careful cheaper than careless. It asks nobody to be
// vigilant, which is the point: the discipline fix was tried at both strengths
// and failed at both, most tellingly when the author of the unconditional rule
// broke it two hours later. Knowing is not a mechanism.
//
// --body is deliberately untouched and NOT deprecated: it is fine for the many
// bodies that carry no metacharacters, and removing it would break every caller
// for whom it already works.
func bodyFromFlags(cmd *cobra.Command, body, bodyFile string) (string, error) {
	bodyGiven := cmd.Flags().Changed("body")
	fileGiven := cmd.Flags().Changed("body-file")

	if bodyGiven && fileGiven {
		return "", fmt.Errorf("cannot use both --body and --body-file: " +
			"pass the body inline with --body, or name a file with --body-file — not both")
	}
	if !fileGiven {
		return body, nil
	}
	return readBodyFile(cmd, bodyFile)
}

// readBodyFile reads the body bytes from path verbatim — no shell, no
// expansion, no interpretation. "-" reads stdin instead.
//
// A path that cannot be read is an error, never an empty body: silently
// spawning a polecat with no task on a typo'd path would be this ticket's own
// disease — an instrument reporting success for work it did not do.
func readBodyFile(cmd *cobra.Command, path string) (string, error) {
	if path == "-" {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("cannot read body from stdin: %w", err)
		}
		return string(data), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read --body-file %q: %w — "+
			"check that the path exists and is a readable file", path, err)
	}
	return string(data), nil
}
