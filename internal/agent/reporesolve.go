package agent

import (
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/config"
)

// RepoResolver turns a repository NAME into the repository PATH the fleet
// counts workers in.
//
// It exists because a work item's `repo` field is free text and is not always a
// path. Measured across the whole item store on 2026-08-20 (mg-cd4a, counted by
// pm-pogo), 42 items carry a bare `pogo`, 108 a bare `one_third_width_three`
// and 47 a bare `union_closed`, against 883 and 262 carrying the corresponding
// absolute paths. Every comparison the per-repo cap makes runs through
// config.SameRepo, which compares cleaned strings — so `pogo` matches no
// worker's SourceRepo, and a saturated repository reports ZERO occupants.
//
// It is an interface, and the project index is reached through it rather than
// imported, for the reason Capacity and RefineryActivity are: internal/agent
// keeps no edge to internal/project, and the resolution can be tested without a
// live index. cmd/pogod supplies the production implementation as a closure
// over the indexed project list.
type RepoResolver interface {
	// ResolveRepo returns the absolute repository path name refers to.
	//
	// ok=false means the name could not be resolved to EXACTLY ONE known
	// repository — no match, or several. It is NOT "there is no such repo and
	// therefore nothing is running in it": a caller that treats an unresolved
	// name as an empty repository reintroduces mg-cd4a.
	ResolveRepo(name string) (path string, ok bool)
}

// RepoResolverFunc adapts a function to RepoResolver.
type RepoResolverFunc func(name string) (string, bool)

// ResolveRepo implements RepoResolver.
func (f RepoResolverFunc) ResolveRepo(name string) (string, bool) { return f(name) }

// SetRepoResolver installs the name→path resolver the per-repo cap consults for
// a `repo` field that is not already an absolute path.
//
// Passing nil leaves relative repo names UNRESOLVED, which is the safe state
// rather than the old one: RepoOccupancyFor then reports occupancy as
// unresolvable instead of reporting it as zero. Callers render that as "could
// not be determined", which is what the fleet actually knows.
func (r *Registry) SetRepoResolver(res RepoResolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repoResolver = res
}

func (r *Registry) getRepoResolver() RepoResolver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.repoResolver
}

// MatchRepoName picks the one known repository path that a relative repo name
// refers to, or reports that it cannot.
//
// The match is a COMPONENT-ALIGNED SUFFIX: `pogo` matches
// `/Users/daniel/dev/pogo` and does NOT match `/Users/daniel/dev/pogo-reminders`,
// and `drellem2/pogo-reminders` matches a path ending in those two components
// in that order. Substring matching would have made `pogo` ambiguous against
// four of this machine's repositories at once, and — worse — silently picked one.
//
// # Why ambiguity answers "no" rather than "probably"
//
// This function feeds a message that TELLS A COORDINATOR WHAT TO DO. A wrong
// resolution does not produce a visible error; it produces a confident sentence
// about the wrong repository's occupancy, which is the same failure mg-cd4a is
// about with a different cause. Two candidates therefore resolve to nothing,
// and the caller says it could not tell — a reader who sees that can check, and
// a reader who sees a confident wrong number cannot.
//
// known may contain duplicates and unnormalized paths; only absolute paths are
// considered, because a relative entry in the index could not disambiguate
// anything anyway.
func MatchRepoName(name string, known []string) (string, bool) {
	want := repoComponents(config.NormalizeRepo(name))
	if len(want) == 0 {
		return "", false
	}
	var hit string
	for _, k := range known {
		p := config.NormalizeRepo(k)
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		if !hasComponentSuffix(repoComponents(p), want) {
			continue
		}
		if hit != "" && hit != p {
			// Ambiguous. See the doc comment: this is deliberately not a
			// "pick the first" — the caller must be able to say it cannot tell.
			return "", false
		}
		hit = p
	}
	if hit == "" {
		return "", false
	}
	return hit, true
}

// repoComponents splits a cleaned path into its non-empty path components.
func repoComponents(p string) []string {
	if p == "" {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(p), "/")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hasComponentSuffix reports whether path ends with every component of suffix,
// in order.
func hasComponentSuffix(path, suffix []string) bool {
	if len(suffix) == 0 || len(suffix) > len(path) {
		return false
	}
	off := len(path) - len(suffix)
	for i, s := range suffix {
		if path[off+i] != s {
			return false
		}
	}
	return true
}
