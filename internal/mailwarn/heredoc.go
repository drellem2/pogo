package mailwarn

import "strings"

// stripHeredocs removes the BODY of every here-document from a shell command,
// leaving the command words around it.
//
// This is not a nicety. `mg mail send --help` makes the quoted heredoc the
// canonical way to write a body — it is the only form that passes bytes through
// untouched — so nearly every real send in this fleet has one, and a body is
// prose that routinely quotes other commands. Without this, mailing the mayor
// ABOUT a dead channel would warn about the dead channel, which is precisely
// the kind of false positive that gets a warning line skipped.
//
// It is quote-aware only enough to not fire on a literal "<<" inside quotes,
// and it treats an unterminated heredoc as running to the end of the command.
// Both are the conservative direction: dropping too much text can only cost a
// warning, while keeping a body can only invent one.
func stripHeredocs(s string) string {
	var out strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(s):
			out.WriteByte(c)
			out.WriteByte(s[i+1])
			i++
			continue
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '<' && !inSingle && !inDouble && i+1 < len(s) && s[i+1] == '<':
			// `<<<` is a here-STRING: one word, no body, nothing to strip.
			if i+2 < len(s) && s[i+2] == '<' {
				out.WriteString("<<<")
				i += 2
				continue
			}
			delim, after, ok := heredocDelimiter(s[i+2:])
			if !ok {
				break
			}
			out.WriteString("<<")
			rest := s[i+2:]
			// Keep the redirection and its delimiter word, drop the body.
			out.WriteString(rest[:after])
			i += 2 + after
			if j := endOfHeredocBody(s[i:], delim); j >= 0 {
				i += j - 1
			} else {
				i = len(s)
			}
			continue
		}
		if i < len(s) {
			out.WriteByte(c)
		}
	}
	return out.String()
}

// heredocDelimiter reads the delimiter word that follows `<<`, returning it
// unquoted along with how many bytes of s it occupied.
func heredocDelimiter(s string) (delim string, n int, ok bool) {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	start := i
	var word strings.Builder
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '|' || c == '&' {
			break
		}
		if c == '\'' || c == '"' {
			q := c
			i++
			for i < len(s) && s[i] != q {
				word.WriteByte(s[i])
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		if c == '\\' && i+1 < len(s) {
			i++
		}
		word.WriteByte(s[i])
		i++
	}
	if word.Len() == 0 || i == start {
		return "", 0, false
	}
	return word.String(), i, true
}

// endOfHeredocBody returns the offset in s just past the terminator line for
// delim, or -1 when the body is unterminated. s begins at the delimiter word's
// end, so the body starts after the first newline.
func endOfHeredocBody(s, delim string) int {
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return -1
	}
	pos := nl + 1
	for pos <= len(s) {
		end := strings.IndexByte(s[pos:], '\n')
		var line string
		if end < 0 {
			line = s[pos:]
		} else {
			line = s[pos : pos+end]
		}
		// `<<-` strips leading tabs from the terminator; trimming whitespace
		// generally is the forgiving direction, and over-matching a terminator
		// only ends the body early.
		if strings.TrimSpace(line) == delim {
			if end < 0 {
				return len(s)
			}
			return pos + end
		}
		if end < 0 {
			return -1
		}
		pos += end + 1
	}
	return -1
}
