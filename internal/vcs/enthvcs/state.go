package enthvcs

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// What an enth checkout records about itself, and what pogo records beside it
// (docs/design/enth-vcs.md §4, slice 5, mg-cc5cd).
//
// enth #9 writes DIR/.enth/checkout on every checkout (enth's
// tests/shell/checkout_ops_test.sh is the contract). JSON lines; the first is
//
//	{"format":"enth-checkout","version":1,"base":"<id>","origin":O,"paths":[B...]}
//
// with O = {"store":B} or {"remote":URL,"repo":NAME|null}, and then one line
// per index entry. B is a JSON string, or {"hex":..} for bytes that are not
// UTF-8.
//
// E6 (§13: the state file records the remote and the repo) turned out to be
// SHIPPED in #9 — origin carries both (measured 2026-11-03 on enth 6488096).
// What it does not carry is which pogo project the checkout was cut for, nor
// the ref it follows, so pogod still writes its own workspace record (below)
// next to it.

// StateRelPath is the checkout's state file, relative to its root. It is the
// enth repo marker (X1, X2), as .git is git's.
var StateRelPath = filepath.Join(".enth", "checkout")

// ExcludeRelPath is the checkout's local exclude list (git's info/exclude):
// read by enth status as a root .gitignore, never part of a Change.
var ExcludeRelPath = filepath.Join(".enth", "info", "exclude")

// RecordRelPath is pogod's workspace record for a checkout it created.
// Inside .enth/ because enth skips .enth at every depth (it is never an op),
// and because the record must die with the checkout: rm -rf DIR removes both.
var RecordRelPath = filepath.Join(".enth", "pogo-workspace.json")

// Origin is where a checkout reads from.
type Origin struct {
	Store  string `json:"store,omitempty"`
	Remote string `json:"remote,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

// State is a checkout's state file, parsed.
type State struct {
	Format  string
	Version int
	Base    string
	Origin  Origin
	// Paths is the selection; empty is the whole tree.
	Paths []string
	// Entries maps each index path to its mode ("file", "exec", "symlink",
	// "gitlink").
	Entries map[string]string
}

type stateHeader struct {
	Format  string            `json:"format"`
	Version int               `json:"version"`
	Base    string            `json:"base"`
	Origin  json.RawMessage   `json:"origin"`
	Paths   []json.RawMessage `json:"paths"`
}

type stateEntry struct {
	Path json.RawMessage `json:"path"`
	Mode string          `json:"mode"`
}

// decodePath reads enth's B: a JSON string, or {"hex":..}.
func decodePath(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var h struct {
		Hex string `json:"hex"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", fmt.Errorf("enth path %s: %w", raw, err)
	}
	b, err := hex.DecodeString(h.Hex)
	if err != nil {
		return "", fmt.Errorf("enth path %s: %w", raw, err)
	}
	return string(b), nil
}

func decodeOrigin(raw json.RawMessage) (Origin, error) {
	var o struct {
		Store  json.RawMessage `json:"store"`
		Remote string          `json:"remote"`
		Repo   *string         `json:"repo"`
	}
	if len(raw) == 0 {
		return Origin{}, nil
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return Origin{}, err
	}
	out := Origin{Remote: o.Remote}
	if o.Repo != nil {
		out.Repo = *o.Repo
	}
	if len(o.Store) > 0 && string(o.Store) != "null" {
		s, err := decodePath(o.Store)
		if err != nil {
			return Origin{}, err
		}
		out.Store = s
	}
	return out, nil
}

// ReadState parses root/.enth/checkout. A missing file is an error satisfying
// errors.Is(err, os.ErrNotExist). A format or version this code does not know
// is an error, never a guess (enth's I14).
func ReadState(root string) (State, error) {
	f, err := os.Open(filepath.Join(root, StateRelPath))
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return State{}, err
		}
		return State{}, fmt.Errorf("%s: empty enth checkout state", filepath.Join(root, StateRelPath))
	}
	var h stateHeader
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return State{}, fmt.Errorf("%s: %w", filepath.Join(root, StateRelPath), err)
	}
	if h.Format != "enth-checkout" || h.Version != 1 {
		return State{}, fmt.Errorf("%s: unsupported enth checkout state (format %q version %d; this pogo reads enth-checkout version 1)",
			filepath.Join(root, StateRelPath), h.Format, h.Version)
	}
	st := State{Format: h.Format, Version: h.Version, Base: h.Base, Entries: map[string]string{}}
	if st.Origin, err = decodeOrigin(h.Origin); err != nil {
		return State{}, fmt.Errorf("%s: origin: %w", filepath.Join(root, StateRelPath), err)
	}
	for _, p := range h.Paths {
		s, err := decodePath(p)
		if err != nil {
			return State{}, err
		}
		st.Paths = append(st.Paths, s)
	}
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e stateEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return State{}, fmt.Errorf("%s: %w", filepath.Join(root, StateRelPath), err)
		}
		p, err := decodePath(e.Path)
		if err != nil {
			return State{}, err
		}
		st.Entries[p] = e.Mode
	}
	if err := sc.Err(); err != nil {
		return State{}, err
	}
	return st, nil
}

// IsCheckout reports whether dir is the root of an enth checkout: it holds a
// regular .enth/checkout file. Cheap (one stat).
func IsCheckout(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, StateRelPath))
	return err == nil && fi.Mode().IsRegular()
}

// AmbiguousError is a directory carrying both repo markers. It is refused
// rather than guessed at (§4, X2): a guess would run one VCS's verbs over a
// tree the other one owns.
type AmbiguousError struct {
	Dir string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%s holds both .git and %s: refusing to guess whether it is a git or an enth checkout", e.Dir, StateRelPath)
}

// Detect names the VCS of the workspace containing dir and that workspace's
// root, by walking up from dir to the nearest repo marker: .git (git) or
// .enth/checkout (enth). The nearest one wins, which is what makes an enth
// checkout nested inside a git repo — every polecat's, since ~/.pogo is one —
// an enth checkout and not a subdirectory of ~/.pogo. A level carrying both is
// an *AmbiguousError.
//
// No marker anywhere is (git, "", nil): the git backend then answers exactly as
// it did before this function existed, which is how the git path stays
// unchanged for every tree that is not an enth checkout.
func Detect(dir string) (vcs.Kind, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return vcs.Git, "", nil
	}
	for cur := abs; ; {
		enth := IsCheckout(cur)
		_, gerr := os.Lstat(filepath.Join(cur, ".git"))
		git := gerr == nil
		switch {
		case enth && git:
			return "", cur, &AmbiguousError{Dir: cur}
		case enth:
			return vcs.Enth, cur, nil
		case git:
			return vcs.Git, cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return vcs.Git, "", nil
		}
		cur = parent
	}
}

// Record is pogod's workspace record for a checkout it created: the facts
// the checkout itself does not carry. It is the "spawn record" §4 names.
type Record struct {
	// Repo is the pogo project (its registered path) the workspace was cut
	// for: what Describe answers as the workspace's repo.
	Repo string `json:"repo"`
	// Remote and EnthRepo are the origin the checkout was made from, as pogod
	// asked for it. .enth/checkout records the same (E6); both are kept so a
	// disagreement is visible rather than resolved silently.
	Remote   string `json:"remote"`
	EnthRepo string `json:"enth_repo"`
	// Ref is the ref the checkout follows (what Refresh re-checks out).
	Ref string `json:"ref"`
	// Base is the Change the checkout was made at (or last refreshed to).
	// A state base that differs from it means a Change was made in the
	// workspace since.
	Base string `json:"base"`
	// Paths is the selection pogod asked for; empty is the whole repo.
	Paths     []string  `json:"paths,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ReadRecord reads root's workspace record. Missing satisfies
// errors.Is(err, os.ErrNotExist).
func ReadRecord(root string) (Record, error) {
	data, err := os.ReadFile(filepath.Join(root, RecordRelPath))
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, fmt.Errorf("%s: %w", filepath.Join(root, RecordRelPath), err)
	}
	return r, nil
}

// WriteRecord writes root's workspace record atomically.
func WriteRecord(root string, r Record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(root, RecordRelPath)
	tmp, err := os.CreateTemp(filepath.Dir(p), ".pogo-workspace-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
