package mailwarn

import "strings"

// token is one word of a shell command, with a flag for whether any part of it
// arrived inside quotes.
//
// The quoted flag is what keeps a HEREDOC BODY from being read as a command.
// `mg mail send` bodies routinely quote other sends — this package's own tests
// do, the incident report that filed mg-d924 does, and a warning triggered by
// prose ABOUT a send is a false alarm that teaches the reader to skip the line.
// Quoting is not a perfect fence (an unquoted heredoc body is still bare
// words), so it is one of several conservative filters rather than the filter.
type token struct {
	text   string
	quoted bool
}

// tokenize splits a shell command into words using the three quoting rules that
// actually appear in agent-written command lines: single quotes, double quotes,
// and backslash escapes. Shell operators come back as their own tokens so a
// scan can stop at them.
//
// This is deliberately NOT a shell parser. It does not expand anything, does
// not track heredoc delimiters, and does not understand subshells. It is a
// splitter good enough to find `mg mail send <name>` in real command text, and
// every place it might be wrong is handled by declining to warn rather than by
// guessing (see positionalAfter).
func tokenize(s string) []token {
	var out []token
	var cur strings.Builder
	started, quoted := false, false

	flush := func() {
		if started {
			out = append(out, token{text: cur.String(), quoted: quoted})
			cur.Reset()
			started, quoted = false, false
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t', '\r', '\n':
			flush()
		case '\'':
			started, quoted = true, true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				cur.WriteString(s[i+1:])
				i = len(s)
				break
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case '"':
			started, quoted = true, true
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				j++
			}
			cur.WriteString(unescape(s[i+1 : min(j, len(s))]))
			i = j
		case '\\':
			if i+1 < len(s) {
				i++
				if s[i] == '\n' {
					break
				}
				started = true
				cur.WriteByte(s[i])
			}
		case '|', '&', ';', '<', '>', '(', ')':
			flush()
			// Operators are emitted whole so `&&` does not read as two tokens.
			j := i
			for j+1 < len(s) && s[j+1] == c {
				j++
			}
			out = append(out, token{text: s[i : j+1]})
			i = j
		default:
			started = true
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func unescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
