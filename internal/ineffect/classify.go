package ineffect

// The PATH -> ARTIFACT CLASS half of the answer (mg-3d0e).
//
// The question this package exists for is "is commit X in effect?", and the
// reason it could not previously be answered by a command is that the answer
// depends on WHAT THE COMMIT TOUCHED. A merge puts bytes on `main`; whether
// those bytes are executing depends on which artifact carries them to a
// runtime, and on this box the carriers move independently:
//
//	compiled Go        reaches a runtime when a binary is rebuilt AND restarted
//	agent prompts      embedded in a binary AND installed to ~/.pogo/agents AND
//	                   read once, at spawn, by each running agent
//	scripts and plists reach a runtime through an installed COPY, or through a
//	                   checkout that executes them in place
//	docs and tests     never reach a runtime at all
//
// WHY CLASSIFICATION IS BY PATH AND CARRIERS ARE BY OBSERVATION. A table
// mapping "scripts/launchd/pogo-deploy.sh" to "~/.pogo/bin/pogo-deploy.sh" is a
// CLAIM, and claims about installer behaviour rot silently — that is the shape
// of the defect this ticket is about, one layer down. So the class says only
// what KIND of carrier to go looking for; which carriers actually exist is
// established by looking at the box (see assess.go and hostdeps.go). A script
// with no installed copy reports its checkouts and says the copy is absent; a
// script that acquires an installed copy tomorrow is picked up without a code
// change.
//
// WHY `unclassified` IS A CLASS AND NOT A DEFAULT TO SOMETHING SAFE-LOOKING. A
// path this file has no rule for is a path whose carrier nobody has thought
// about. Folding it into "no runtime carrier" would render an unexamined file
// as a documentation change and put a green row next to it. It renders as
// UNKNOWN, which is the only honest thing an unrecognised path can produce.

import (
	"path"
	"strings"
)

// Class is the MECHANISM by which a changed file reaches a runtime. It is not
// the carrier — one class can have several carriers, and how many a given path
// has is a fact about this box, not about the path.
type Class string

const (
	// ClassCompiled — Go source. Reaches a runtime only by being compiled into
	// a binary, and only once that binary is both rebuilt and re-executed.
	ClassCompiled Class = "compiled"
	// ClassPrompt — the shipped agent-prompt corpus. THREE carriers, which is
	// why it is not merged into ClassCompiled: the bytes are embedded in a
	// binary (go:embed), installed as files under ~/.pogo/agents, and read by
	// each running agent exactly once, at spawn. The third is why a prompt
	// change can never be reported as flatly `live` (see mg-385f).
	ClassPrompt Class = "agent-prompt"
	// ClassAsset — a script, plist or hook: read at run time, either from an
	// installed copy or from a checkout, never compiled.
	ClassAsset Class = "runtime-asset"
	// ClassNoCarrier — documentation, changelog fragments, tests and test
	// fixtures. Nothing executes these in production; "in effect" has no
	// meaning for them and the report says so rather than saying `live`.
	ClassNoCarrier Class = "no-runtime-carrier"
	// ClassUnclassified — no rule matched. NOT a pass, NOT a no-carrier.
	ClassUnclassified Class = "unclassified"
)

// classOrder is the render order: the classes whose carriers can be stale come
// first, because they are what the reader is deciding on.
var classOrder = []Class{ClassCompiled, ClassPrompt, ClassAsset, ClassUnclassified, ClassNoCarrier}

// PromptsSubtree is the repo prefix of the shipped prompt corpus. It is
// duplicated from internal/staleness.PromptsSubtree rather than imported to
// keep this package free of that one's git/network machinery; classify_test.go
// asserts the two agree, so a move cannot make them differ quietly.
const PromptsSubtree = "internal/agent/prompts/"

// PromptEmbedPkg is the package whose `//go:embed prompts` directive compiles
// the corpus into every binary that imports it. Asserted against the real
// directive by TestPromptEmbedPkgHasTheDirective — a constant naming a build
// fact that nothing checks is exactly the kind of claim this package refuses to
// make elsewhere.
const PromptEmbedPkg = "internal/agent"

// Classify maps one repo-relative path to the mechanism that carries it.
//
// Order matters: the prompt corpus lives under internal/ but is not compiled
// Go, and test files live beside shipped Go but are compiled into nothing that
// ships. Both have to be decided before the coarse rules see them.
func Classify(p string) Class {
	p = strings.TrimPrefix(path.Clean(p), "./")
	base := path.Base(p)

	switch {
	// Test material first: a _test.go file sits in a shipped package and is in
	// no shipped binary. Classifying it as `compiled` would report a carrier
	// that provably does not carry it.
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.sh"),
		hasSegment(p, "testdata"), hasSegment(p, "_testdata"),
		hasSegment(p, "testfixtures"):
		return ClassNoCarrier

	case strings.HasPrefix(p, PromptsSubtree):
		return ClassPrompt

	case strings.HasSuffix(base, ".go"):
		return ClassCompiled

	case strings.HasSuffix(base, ".sh"),
		strings.HasSuffix(base, ".plist"),
		strings.HasPrefix(p, "hooks/"),
		strings.HasPrefix(p, ".github/"),
		strings.HasPrefix(p, "scripts/"):
		return ClassAsset

	case strings.HasSuffix(base, ".md"),
		strings.HasSuffix(base, ".txt"),
		strings.HasPrefix(p, "docs/"),
		strings.HasPrefix(p, "changelog.d/"),
		strings.HasPrefix(base, "LICENSE"):
		return ClassNoCarrier

	// go.mod / go.sum change what a REBUILD produces; they are carried by the
	// same binaries as the Go source that imports them.
	case base == "go.mod", base == "go.sum":
		return ClassCompiled

	default:
		return ClassUnclassified
	}
}

// hasSegment reports whether seg appears as a whole path element.
func hasSegment(p, seg string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == seg {
			return true
		}
	}
	return false
}

// PkgDir is the Go package directory a compiled path belongs to — the input the
// dependency-graph lookup takes. Returns "" for a path that is not Go source.
func PkgDir(p string) string {
	p = strings.TrimPrefix(path.Clean(p), "./")
	if !strings.HasSuffix(p, ".go") {
		return ""
	}
	dir := path.Dir(p)
	if dir == "." {
		return ""
	}
	return dir
}

// InstalledPromptPath maps a repo prompt path to its path under an installed
// corpus root, mirroring the layout InstallPrompts writes. Returns "" for a
// path outside the corpus.
func InstalledPromptPath(root, p string) string {
	p = strings.TrimPrefix(path.Clean(p), "./")
	if !strings.HasPrefix(p, PromptsSubtree) {
		return ""
	}
	return path.Join(root, strings.TrimPrefix(p, PromptsSubtree))
}
