package memcheck

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PER-AGENT STORES — the fourth way an auto-memory store fails, and the only one
// of the four that is invisible from inside EVERY session rather than just the
// affected one.
//
//	OVER-CAP TRUNCATION  the dropped entries never arrive.                <- Check/CheckFile
//	AN UNINDEXED NOTE    nothing points at it.                            <- parity.go
//	A STALE HOOK         it reads as current.                             <- staleness.go
//	AN ABANDONED STORE   nothing loads it, and every OTHER store is fine. <- THIS FILE
//
// The first three are properties of one store that some session is still using.
// This one is a property of a store no session uses any more, which means no
// session can report it: the agents that used to write here have healthy recall
// against a different store, and their indexes are at exact parity. Nothing is
// broken anywhere an instrument is pointed.
//
// HOW A STORE BECOMES ABANDONED, measured rather than theorised. A harness keys
// its per-session store on the session's PROJECT, and for a directory inside a
// git repo the project is the repo, not the directory. `~/.pogo` became a git
// repo on 2026-07-07. Before that, each crew agent running from
// `~/.pogo/agents/<name>` resolved to its own project and got its own store;
// after it, they all resolved to `~/.pogo` and shared one. The collapse was an
// improvement. What nobody noticed is that 153 notes across five stores stayed
// behind — not duplicates (of one agent's 57, zero were also in the shared
// store) and not lost either: every file was on disk and readable the whole
// time. They simply stopped participating in recall.
//
// It cost a duplicated investigation. One of the stranded notes, dated
// 2026-07-30, recorded a finding that two agents re-derived from scratch two
// weeks later and filed as new work (mg-b6bd, then mg-a9b3). That is one note of
// 153, and it is only the one somebody tripped over.
//
// WHY THIS IS NOT AN AGE CHECK. The obvious detector — "a store nothing has
// written to in N days" — fires on every legitimately dormant per-repo store on
// the machine, of which this box has several, and it also cannot see a store
// stranded five minutes ago. The signal is not staleness. It is that the store
// is keyed to a directory POGO ITSELF OWNS: pogo creates `~/.pogo/agents/<name>`
// and runs the agent there, so a harness store hanging off that directory has
// exactly one possible reader by construction. A shared store has many. Once the
// fleet's memories live in one shared store, a populated per-agent store is a
// store with no reader, whatever its mtime says.
//
// AND WHY THE PATH IS CONSTRUCTED, NOT DISCOVERED. Locate globs the machine and
// finds every store; it cannot say which agent a store belongs to. Going the
// other way — agent working directory -> store — needs the harness's own path
// encoding, which pogo does not own. So the encoding stays in the provider
// (agent.Provider.AgentMemoryStoreIndex) and a wrong model here produces a path
// that does not exist rather than a wrong finding. That is the safe failure
// direction, and it is exactly why the caller must not read "no store matched"
// as "no store had a problem": see Survey.Measured.

// Store is one harness auto-memory store belonging to one pogo-owned agent
// directory.
type Store struct {
	// Agent is the agent-directory name under pogo's agent root — the name pogo
	// used when it created the working directory, not necessarily a configured
	// agent (see Survey for why the enumeration is deliberately loose).
	Agent string

	// WorkDir is the absolute agent working directory the store is keyed on.
	WorkDir string

	// IndexPath is the absolute path of the store's MEMORY.md.
	IndexPath string

	// Dir is the absolute store directory (IndexPath's parent).
	Dir string

	// Notes are the base names of the note files in the store, sorted. Index
	// files are excluded: MEMORY.md itself, and any `_`-prefixed file, which is
	// this corpus's convention for a secondary index or an archive rather than a
	// recall note.
	Notes []string

	// Newest is the modification time of the most recently written note, zero
	// when Notes is empty.
	Newest time.Time
}

// Stranded reports whether this store holds notes that no session will load.
//
// A store with a MEMORY.md and no notes is NOT stranded — that is the shape a
// retired store is deliberately left in, so that the tombstone explaining the
// retirement survives and the next reader finds it instead of re-deriving why
// the directory is empty. Removing the directory outright would only mean the
// next session on this project root re-creates it silently.
func (s Store) Stranded() bool { return len(s.Notes) > 0 }

// Survey is the result of one sweep over pogo's agent root.
type Survey struct {
	// Stores are the per-agent stores that exist on disk, sorted by agent name
	// then index path. A constructed path that does not exist is not a store and
	// does not appear here.
	Stores []Store

	// Candidates counts the (agent directory, provider) pairs whose store path
	// was constructed and probed, whether or not anything was there. It is the
	// denominator that separates "checked and clean" from "checked nothing".
	Candidates int
}

// Stranded returns the stores holding notes, in Stores order.
func (s Survey) Stranded() []Store {
	var out []Store
	for _, st := range s.Stores {
		if st.Stranded() {
			out = append(out, st)
		}
	}
	return out
}

// Measured reports whether the sweep actually probed anything.
//
// False means one of two things and the check cannot tell them apart from here:
// pogo's agent root holds no agent directories, or every provider declined to
// name a store for them. Either way the correct report is "unmeasured", never
// "clean" — a detector whose path model has silently stopped matching produces a
// zero that looks exactly like a healthy population.
func (s Survey) Measured() bool { return s.Candidates > 0 }

// SurveyPerAgentStores probes, for every agent working directory pogo owns under
// agentRoot, the auto-memory stores a harness would key on it.
//
// home is the user's home directory; the paths indexFor returns are interpreted
// relative to it, matching how providers declare them. indexFor maps an absolute
// agent working directory to zero or more home-relative index paths — in
// practice providers.AgentMemoryStoreIndexes, passed in so this package names no
// harness's dotdir (the same discipline Locate follows).
//
// The enumeration is every immediate subdirectory of agentRoot, deliberately
// unfiltered against the configured roster. pogo creates those directories when
// it spawns agents, including polecats spawned with --no-worktree, and an agent
// that has since been removed from the config is exactly the case whose store
// nobody is going to notice. Filtering to the roster would make the check blind
// to its most likely finding. Non-agent directories cost nothing: no store is
// keyed on them, so no path constructed for them exists.
//
// A missing agentRoot yields an empty, unmeasured Survey rather than an error —
// there is nothing to say about a machine that has never run an agent.
func SurveyPerAgentStores(home, agentRoot string, indexFor func(workdir string) []string) (Survey, error) {
	var sv Survey
	if indexFor == nil {
		return sv, nil
	}
	entries, err := os.ReadDir(agentRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return sv, nil
		}
		return sv, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		workDir := filepath.Join(agentRoot, e.Name())
		for _, rel := range indexFor(workDir) {
			sv.Candidates++
			indexPath := filepath.Join(home, filepath.FromSlash(rel))
			if _, err := os.Stat(indexPath); err != nil {
				continue
			}
			dir := filepath.Dir(indexPath)
			notes, newest := storeNotes(dir)
			sv.Stores = append(sv.Stores, Store{
				Agent:     e.Name(),
				WorkDir:   workDir,
				IndexPath: indexPath,
				Dir:       dir,
				Notes:     notes,
				Newest:    newest,
			})
		}
	}
	sort.SliceStable(sv.Stores, func(i, j int) bool {
		if sv.Stores[i].Agent != sv.Stores[j].Agent {
			return sv.Stores[i].Agent < sv.Stores[j].Agent
		}
		return sv.Stores[i].IndexPath < sv.Stores[j].IndexPath
	})
	return sv, nil
}

// storeNotes lists the recall notes in a store directory and the newest one's
// mtime. Only the top level is read: this corpus keeps retired originals in
// subdirectories, and those are archived rather than live, so counting them
// would report a store as populated forever after it was correctly emptied.
func storeNotes(dir string) ([]string, time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, time.Time{}
	}
	var notes []string
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || name == "MEMORY.md" || strings.HasPrefix(name, "_") {
			continue
		}
		notes = append(notes, name)
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	sort.Strings(notes)
	return notes, newest
}
