// Package investigations searches the point-in-time reports under
// docs/investigations/ by their CONTENTS.
//
// WHY THIS EXISTS (mg-22c7). On the night of 2026-08-11/12, four of the five
// "the artifact already had the answer" moments were files in this one
// directory, and none of them were found: three agents re-derived, mis-derived
// and retracted answers the corpus states in a line, and one proposal would have
// taken the live fleet down to measure something already written up. The cost is
// not the wasted turns. A discoverability failure MANUFACTURES CONFIDENT FALSE
// CLAIMS, and those travel between agents until someone spends a turn undoing
// them.
//
// WHY IT SEARCHES FILES AND NOT THE INDEX. The obvious build is to grep the
// Covers/Outcome columns of docs/investigations/README.md, and that build was
// specified, reviewed, and caught before it shipped. README.md is maintained by
// hand and at the time of writing it omitted 10 of 45 files — and the omissions
// are not random: the newest investigations are the least likely to be indexed,
// which is to say a README search would be worst exactly where it is most
// needed. One of the ten answered a ticket that had sat available for five days
// asking for that work.
//
// The gap is worse than missing rows. A searcher who queries an incomplete index
// and gets nothing concludes NO INVESTIGATION EXISTS — a licence to answer "no"
// issued by an instrument that cannot see the candidate space. This package
// therefore searches the files on disk, treats README.md as a document about the
// corpus rather than as the corpus, and reports index coverage as a diagnostic
// so that "the index is complete" is never quietly assumed again.
//
// Everything it declines to search is reported (see Result.Skipped). A search
// that silently drops files reproduces the bug it exists to fix, one level down.
package investigations

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultDir is the corpus location relative to a repository root.
const DefaultDir = "docs/investigations"

// indexName is the hand-maintained index inside the corpus directory. It is
// excluded from the search domain: it describes the files rather than being one
// of them, and matching it would report the index row instead of the
// investigation, which is the failure this package was built to end.
const indexName = "README.md"

// binarySniffBytes is how much of a file is inspected for a NUL byte before
// deciding it is not text. Text investigations are markdown; anything binary
// that lands here is reported as skipped rather than scanned for lines.
const binarySniffBytes = 8192

// Match is one line of a document that contains at least one query term.
type Match struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Doc is one investigation that satisfied the query.
type Doc struct {
	File    string  `json:"file"`  // path relative to the corpus directory
	Path    string  `json:"path"`  // absolute path, so the reader can open it
	Title   string  `json:"title"` // first markdown H1, else the filename stem
	Meta    string  `json:"meta,omitempty"`
	Indexed bool    `json:"indexed"` // mentioned by README.md
	Hits    int     `json:"hits"`    // total matching lines, before truncation
	Matches []Match `json:"matches"`
}

// Skip records a directory entry that was NOT searched, and why. It is part of
// the result rather than a log line: a denominator that omits what it could not
// read is the same defect as an index that omits what nobody added to it.
type Skip struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Result is one search, including everything needed to state its denominator.
type Result struct {
	Dir   string   `json:"dir"`
	Query []string `json:"query"`

	// Searched is the number of files whose contents were actually read.
	Searched int `json:"searched"`
	// Indexed is how many of Searched are mentioned by README.md. It is a
	// diagnostic, never a filter: Searched is the search domain.
	Indexed int `json:"indexed"`
	// Unindexed lists the searched files README.md does not mention, in the
	// order they were walked. These are the files an index search would miss.
	Unindexed []string `json:"unindexed"`
	// IndexReadErr is non-empty when README.md could not be read, in which case
	// Indexed and Unindexed say nothing and must not be reported as zero.
	IndexReadErr string `json:"index_read_error,omitempty"`

	Skipped []Skip `json:"skipped"`
	Docs    []Doc  `json:"docs"`
}

// FindCorpus walks up from start looking for a DefaultDir directory, returning
// its absolute path. It exists so the command works from any polecat worktree
// without the caller knowing where the repository root is.
func FindCorpus(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		cand := filepath.Join(dir, DefaultDir)
		if fi, statErr := os.Stat(cand); statErr == nil && fi.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s directory in %s or any parent", DefaultDir, start)
		}
		dir = parent
	}
}

// Search reads every file under dir and returns those containing ALL terms,
// most hits first. Terms are matched case-insensitively as literal substrings
// against the file's contents AND its path, so a query naming a filename
// fragment finds the file even when the word never appears in the prose.
//
// An empty term list matches every document, which is the "show me the corpus"
// mode — a listing that beats reading the index precisely because it is derived
// from the files.
//
// maxMatches caps the Match lines carried per document; Doc.Hits always reports
// the untruncated count, so a truncated listing cannot be misread as a complete
// one. maxMatches <= 0 means no cap.
func Search(dir string, terms []string, maxMatches int) (*Result, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if fi, statErr := os.Stat(abs); statErr != nil || !fi.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", abs)
	}

	res := &Result{Dir: abs, Query: terms, Skipped: []Skip{}, Docs: []Doc{}, Unindexed: []string{}}

	indexed, indexErr := readIndex(filepath.Join(abs, indexName))
	if indexErr != nil {
		res.IndexReadErr = indexErr.Error()
	}

	lowered := make([]string, 0, len(terms))
	for _, t := range terms {
		if t = strings.TrimSpace(t); t != "" {
			lowered = append(lowered, strings.ToLower(t))
		}
	}

	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			res.Skipped = append(res.Skipped, Skip{File: relOr(abs, path), Reason: err.Error()})
			return nil //nolint:nilerr // an unreadable entry is reported, not fatal
		}
		name := d.Name()
		if d.IsDir() {
			if path != abs && strings.HasPrefix(name, ".") {
				res.Skipped = append(res.Skipped, Skip{File: relOr(abs, path), Reason: "hidden directory"})
				return fs.SkipDir
			}
			return nil
		}
		rel := relOr(abs, path)
		if rel == indexName {
			return nil // the index, not an investigation; see indexName
		}
		if strings.HasPrefix(name, ".") {
			res.Skipped = append(res.Skipped, Skip{File: rel, Reason: "hidden file"})
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			res.Skipped = append(res.Skipped, Skip{File: rel, Reason: readErr.Error()})
			return nil
		}
		sniff := data
		if len(sniff) > binarySniffBytes {
			sniff = sniff[:binarySniffBytes]
		}
		if bytes.IndexByte(sniff, 0) >= 0 {
			res.Skipped = append(res.Skipped, Skip{File: rel, Reason: "binary file"})
			return nil
		}

		res.Searched++
		inIndex := indexed[strings.ToLower(name)]
		if indexErr == nil {
			if inIndex {
				res.Indexed++
			} else {
				res.Unindexed = append(res.Unindexed, rel)
			}
		}

		content := string(data)
		hay := strings.ToLower(rel + "\n" + content)
		for _, t := range lowered {
			if !strings.Contains(hay, t) {
				return nil
			}
		}

		doc := Doc{File: rel, Path: path, Indexed: inIndex}
		doc.Title, doc.Meta = headline(content, name)
		doc.Matches, doc.Hits = matchingLines(content, lowered, maxMatches)
		res.Docs = append(res.Docs, doc)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.SliceStable(res.Docs, func(i, j int) bool {
		if res.Docs[i].Hits != res.Docs[j].Hits {
			return res.Docs[i].Hits > res.Docs[j].Hits
		}
		return res.Docs[i].File < res.Docs[j].File
	})
	return res, nil
}

// mdLinkRe matches any markdown filename mentioned in the index, in a link
// target or as bare text. It is deliberately loose: the question this answers is
// "would an index search have found this file", and any mention at all would.
var mdLinkRe = regexp.MustCompile(`[A-Za-z0-9._-]+\.(?:md|yaml|yml|txt|json)`)

func readIndex(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, m := range mdLinkRe.FindAllString(string(data), -1) {
		set[strings.ToLower(m)] = true
	}
	return set, nil
}

// headline returns the document's H1 and its byline (the `**Work item:** ...`
// line these reports open with), so a hit is identifiable without opening it.
func headline(content, fallbackName string) (title, meta string) {
	title = strings.TrimSuffix(fallbackName, filepath.Ext(fallbackName))
	lines := strings.SplitN(content, "\n", 32)
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(ln, "# "))
			continue
		}
		if meta == "" && strings.HasPrefix(ln, "**") {
			meta = ln
		}
		if meta != "" {
			break
		}
	}
	return title, meta
}

// matchingLines returns up to maxMatches lines containing any term, and the
// untruncated count of such lines. With no terms — the listing mode — there are
// no matching lines rather than every line: a listing is about the documents.
func matchingLines(content string, lowered []string, maxMatches int) ([]Match, int) {
	out := []Match{}
	total := 0
	if len(lowered) == 0 {
		return out, 0
	}
	for i, ln := range strings.Split(content, "\n") {
		low := strings.ToLower(ln)
		hit := false
		for _, t := range lowered {
			if strings.Contains(low, t) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		total++
		if maxMatches > 0 && len(out) >= maxMatches {
			continue
		}
		out = append(out, Match{Line: i + 1, Text: strings.TrimSpace(ln)})
	}
	return out, total
}

func relOr(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
