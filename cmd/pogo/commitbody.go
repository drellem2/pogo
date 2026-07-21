package main

import "strings"

// stripGitComments removes the `#`-prefixed lines git puts in COMMIT_EDITMSG
// and strips again before storing the message.
//
// Without this the hook would judge text GitHub never sees. The default
// template is full of it — "# Changes to be committed:" and friends — and a
// branch named `fix-89` shows up there as `# On branch fix-89`, which is not a
// reference at all. Worse, `git commit -v` appends the entire diff below a
// scissors line, so any source file containing a closing keyword near a `#N`
// would fail the author's commit for a string in someone else's code.
//
// Note the asymmetry with the refinery gate, which does NOT strip: by the time
// it looks, git has already removed comments, and whatever survives is the
// stored message verbatim.
func stripGitComments(message string) string {
	// `git commit -v` puts everything after the scissors line — the diff —
	// outside the message. Cut there first.
	if i := strings.Index(message, "\n# ------------------------ >8 ------------------------\n"); i >= 0 {
		message = message[:i]
	}
	lines := strings.Split(message, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
