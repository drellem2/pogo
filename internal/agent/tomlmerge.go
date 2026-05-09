package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TOML drop-in merge for `pm/<instance>.toml` configs.
//
// The merge is a tiny line-level parser that preserves every source line
// (comments, blank lines, multi-line array continuations) verbatim so the
// inlined TOML block in a synthesized PM prompt looks like a hand-written
// config rather than a rebuilt-from-AST one. Merge semantics follow the
// design (docs/prompt-customization-design.md §A): scalars overwritten, arrays
// REPLACED (not appended), new keys added, deep-merged within `[table]`
// sections. Lexical order across drop-in files is later-wins.
//
// Scope intentionally narrow: handles the shapes pogo's pm/<instance>.toml
// files use today (top-level keys plus optional `[table]` sections, scalars +
// single- or multi-line arrays). `[[array_of_tables]]`, dotted bare keys
// outside a table header, and inline-table merging are not supported — if
// they appear they get treated as opaque blocks under the section in which
// they were declared, which is safe (they pass through) but won't merge
// field-by-field. Pm config schema doesn't use those today.

// tomlEntry is one logical key=value record from a TOML file. Lines holds
// the verbatim source lines (including any continuation lines for a
// multi-line array). LeadingComments holds the comment/blank lines that
// appeared immediately before the key — they travel with the entry on
// override so the override doesn't leave dangling comments above an unrelated
// key in the merged output.
type tomlEntry struct {
	Key             string
	Lines           []string
	LeadingComments []string
}

// tomlSection is a `[name]` block (or the implicit top-level when Name == "")
// containing zero or more entries plus the verbatim header lines (comments
// before `[name]` plus the `[name]` line itself).
type tomlSection struct {
	Name             string
	HeaderLines      []string
	Entries          []*tomlEntry
	TrailingComments []string
}

type tomlDoc struct {
	Sections []*tomlSection
}

func (d *tomlDoc) findSection(name string) *tomlSection {
	for _, s := range d.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// MergeTOMLDropIns reads every *.toml file under dir in lexical order and
// returns the base TOML data with each drop-in merged on top, later-wins.
// Scalars and arrays are replaced wholesale per key; new keys are appended to
// the matching section (or the file is extended with a fresh `[name]` block
// when the drop-in introduces a new section).
//
// An absent or empty drop-in dir returns base unchanged with names == nil.
// Any leading pogo-prompt stamp on either the base or a drop-in is stripped
// before parsing so the merged output is itself stamp-free (callers re-stamp
// downstream as needed).
func MergeTOMLDropIns(base []byte, dir string) ([]byte, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil, nil
		}
		return nil, nil, fmt.Errorf("read TOML drop-in dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return base, nil, nil
	}

	baseDoc, err := parseTOMLDoc(stripPromptHashStamp(base))
	if err != nil {
		return nil, nil, fmt.Errorf("parse base TOML: %w", err)
	}
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, nil, fmt.Errorf("read TOML drop-in %s: %w", n, err)
		}
		dropDoc, err := parseTOMLDoc(stripPromptHashStamp(data))
		if err != nil {
			return nil, nil, fmt.Errorf("parse TOML drop-in %s: %w", n, err)
		}
		mergeTOMLDocs(baseDoc, dropDoc)
	}
	return baseDoc.Bytes(), names, nil
}

// parseTOMLDoc tokenizes data into ordered sections + entries with verbatim
// source lines preserved. The grammar is a strict subset of TOML — see file
// header comment for what's covered.
func parseTOMLDoc(data []byte) (*tomlDoc, error) {
	doc := &tomlDoc{}
	cur := &tomlSection{Name: ""}
	var buf []string

	flushBuf := func() []string {
		out := buf
		buf = nil
		return out
	}

	lines := splitTOMLLines(data)
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			buf = append(buf, line)
			i++
			continue
		}

		if strings.HasPrefix(trimmed, "[") {
			// Close the section we were building, open a new one. Top-level
			// (cur.Name == "") with no entries and no buffered comments is
			// kept too — it preserves any leading file header comments above
			// the first `[name]`. Header line + leading comments live on the
			// *new* section so they travel with it on emit.
			cur.TrailingComments = nil // any trailing buf belongs to next section's header instead
			doc.Sections = append(doc.Sections, cur)

			name := parseTOMLTableName(trimmed)
			cur = &tomlSection{
				Name:        name,
				HeaderLines: append(flushBuf(), line),
			}
			i++
			continue
		}

		// key = value (possibly multi-line if value opens a bracket)
		eq := strings.IndexByte(line, '=')
		if eq == -1 {
			// Unparseable non-comment line — keep in buffer as a passthrough
			// so we don't drop user content. Won't be addressable for merge,
			// but won't be lost either.
			buf = append(buf, line)
			i++
			continue
		}
		key := strings.TrimSpace(line[:eq])
		valLines := []string{line}
		depth := tomlBracketDepth(line[eq+1:])
		for depth > 0 && i+1 < len(lines) {
			i++
			valLines = append(valLines, lines[i])
			depth += tomlBracketDepth(lines[i])
		}
		cur.Entries = append(cur.Entries, &tomlEntry{
			Key:             key,
			Lines:           valLines,
			LeadingComments: flushBuf(),
		})
		i++
	}
	cur.TrailingComments = flushBuf()
	doc.Sections = append(doc.Sections, cur)
	return doc, nil
}

// splitTOMLLines splits data into lines without their terminators. A trailing
// newline produces a trailing empty element only when there was an explicit
// blank line at end-of-file (not when the file simply ended with \n) — that
// keeps round-tripping `key=val\n` from emitting a spurious extra blank.
func splitTOMLLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	// Normalize CRLF → LF so quote-state tracking doesn't see a stray \r.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	hasTrailingNL := strings.HasSuffix(s, "\n")
	if hasTrailingNL {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

// parseTOMLTableName extracts the inner table name from a header line like
// `[server]` or `[server.tls] # comment`. `[[name]]` array-of-tables headers
// are returned with the leading `[` preserved (e.g. "[name") so they cannot
// silently collide with a `[name]` table — the merge treats them as opaque
// distinct sections.
func parseTOMLTableName(line string) string {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") {
		return ""
	}
	// Strip a trailing comment (after a #) so `[server] # ...` parses.
	if hash := indexUnquotedHash(s); hash != -1 {
		s = strings.TrimSpace(s[:hash])
	}
	// Match the closing ] for `[name]`, or `]]` for `[[name]]`.
	if strings.HasPrefix(s, "[[") && strings.HasSuffix(s, "]]") {
		// Preserve the [[ to keep array-of-tables distinct from same-name [table].
		return "[" + strings.TrimSpace(s[2:len(s)-2])
	}
	end := strings.IndexByte(s, ']')
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(s[1:end])
}

// indexUnquotedHash returns the byte offset of the first `#` in s that is
// not inside a quoted string, or -1 if none.
func indexUnquotedHash(s string) int {
	inStr := false
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				inStr = false
			}
			continue
		}
		if c == '"' || c == '\'' {
			inStr = true
			quote = c
			continue
		}
		if c == '#' {
			return i
		}
	}
	return -1
}

// tomlBracketDepth returns the net change in bracket nesting introduced by s
// (positive when more `[`/`{` were opened than closed). Quoted strings and
// `#` comments are skipped so brackets inside them don't count. Used to
// detect when a value spans multiple lines (a multi-line array continues
// until the bracket count balances).
func tomlBracketDepth(s string) int {
	depth := 0
	inStr := false
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				inStr = false
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = true
			quote = c
		case '#':
			return depth
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
	}
	return depth
}

// mergeTOMLDocs applies dropin onto base in-place using later-wins semantics:
// for each section in dropin, find the same-named section in base (or append
// it), then for each entry, replace the same-keyed entry in the matched
// section (or append it).
//
// Section identity is exact-string on Name; this means `[server]` and
// `[server.tls]` are distinct sections (no implicit nesting), which matches
// the pm config schema. Within a section, key identity is also exact-string
// — quoted keys (`"name with spaces"`) compare against unquoted keys
// literally rather than after dequoting, which is fine because pogo's pm
// configs use only bare identifier keys.
func mergeTOMLDocs(base, dropin *tomlDoc) {
	for _, dsec := range dropin.Sections {
		bsec := base.findSection(dsec.Name)
		if bsec == nil {
			// Dropin introduced a brand-new section — append it as-is so its
			// header/comments come along.
			base.Sections = append(base.Sections, dsec)
			continue
		}
		for _, dentry := range dsec.Entries {
			replaced := false
			for i, bentry := range bsec.Entries {
				if bentry.Key == dentry.Key {
					bsec.Entries[i] = dentry
					replaced = true
					break
				}
			}
			if !replaced {
				bsec.Entries = append(bsec.Entries, dentry)
			}
		}
	}
}

// Bytes serializes the document back to TOML text. Each section's header
// lines, entries (with leading comments + verbatim value lines), and trailing
// comments are emitted in order, separated by `\n`. The output ends with a
// single trailing newline so it concatenates cleanly inside a fenced TOML
// block.
func (d *tomlDoc) Bytes() []byte {
	var buf bytes.Buffer
	for _, sec := range d.Sections {
		for _, l := range sec.HeaderLines {
			buf.WriteString(l)
			buf.WriteByte('\n')
		}
		for _, e := range sec.Entries {
			for _, l := range e.LeadingComments {
				buf.WriteString(l)
				buf.WriteByte('\n')
			}
			for _, l := range e.Lines {
				buf.WriteString(l)
				buf.WriteByte('\n')
			}
		}
		for _, l := range sec.TrailingComments {
			buf.WriteString(l)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}
