package promptcli

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Rule names a class of defect. They are the three shapes the mg-21b1 fixtures
// take, and each one shipped.
const (
	// RuleUnknownFlag: the prompt tells you to pass a flag the command has not
	// got. mg-d8ea (`mg spend --by item --tag=<tag>`).
	RuleUnknownFlag = "unknown-flag"
	// RuleUnknownSubcommand: the prompt names a subcommand of a command group
	// that the group has not got.
	RuleUnknownSubcommand = "unknown-subcommand"
	// RuleFalseAbsence: the prompt says something does not exist, and it does.
	// mg-4bb9 (`mg edit` "has no append/comment subcommand").
	RuleFalseAbsence = "false-absence"
	// RuleBadOmission: the prompt tells you to leave off a flag the CLI refuses
	// to default. mg-159a (omit `--template` for a bare `task`).
	RuleBadOmission = "bad-omission"
	// RuleUnknownFlagValue: the flag exists and the VALUE is refused. mg-9324
	// (`mg list --status=closed`, where the accepted spelling is `done`).
	// Reported only for a flag a ValueRule covers; see ValueRule and Coverage.
	RuleUnknownFlagValue = "unknown-flag-value"
)

// Finding is one prompt statement the CLI contradicts.
type Finding struct {
	File     string
	Line     int
	Rule     string
	Subject  string // the thing that is wrong, e.g. "mg spend --tag"
	Detail   string
	Evidence string // the prompt text it came from
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d [%s] %s — %s", f.File, f.Line, f.Rule, f.Subject, f.Detail)
}

// OmissionRule answers a question `--help` cannot: is leaving this flag off
// actually accepted?
//
// Cobra prints no conditional-requirement information, so the omission arm of
// the check cannot be derived from the help text the way the other three are.
// It is a small registry instead, and each entry must be backed by the code
// that does the refusing — never by a second copy of the rule. The pogo entry
// (see cmd/pogo) calls agent.TemplateForType, so if the routing map grows a
// `task` entry tomorrow the rule stops firing on its own.
//
// One entry today. It is an extension point, not a general mechanism, and it is
// honest to say so: an omission instruction for a flag with no rule here is not
// checked at all.
type OmissionRule struct {
	Path []string // {"pogo","agent","spawn-polecat"}
	Flag string   // "template", without the dashes
	// AcceptedWithout reports whether omitting the flag is accepted for a
	// scope value the prompt named (a work-item `type`). The empty string means
	// "the prompt scoped this to anything else / the default".
	AcceptedWithout func(scope string) bool
	// Why is quoted into the finding so a reader gets the reason, not just a
	// verdict.
	Why string
}

// ValueRule answers the other question `--help` cannot: is this flag's VALUE
// accepted?
//
// mg-9324's finding is that the legal values are essentially not declared
// anywhere machine-readable — 4 of 153 value-bearing flags spell out an
// enumeration, and one of those four is stale in the direction that invents
// false findings. ParseFlagSpecs has the measurement. So this is a registry for
// the same reason OmissionRule is one, and it carries the same obligation:
//
// EVERY ENTRY MUST NAME THE CODE THAT DOES THE REFUSING, never a second copy of
// the value list. A registry that restates the values is a list that drifts, and
// a drifted value list flags correct prompts.
//
// Two sources are sound, in this order of preference:
//
//   - Accepts: the tool's own accepting function, called directly. Available for
//     `pogo`, because it is this binary. Cannot drift at all.
//   - FromHelp: the enumeration in the flag's own usage string, read at check
//     time — sound only where the tool GENERATES that string from its validator,
//     which the entry's Why must state with the file and line. `mg list
//     --status` qualifies: macguffin renders it from the same
//     `listStatusValues` slice it rejects against. `--provider` does NOT, and is
//     the worked example of why this field needs justifying.
//
// A value-bearing flag with no entry here is NOT CHECKED, and is reported by
// name in Coverage.Unchecked. That reporting is the point: the failure mode of
// this ticket family is a partial check that converts "unchecked" into "checked
// and fine".
type ValueRule struct {
	Path []string // {"mg","list"}
	Flag string   // "status", without the dashes
	// Accepts reports whether the tool accepts this value, by calling the code
	// that decides. Takes precedence over FromHelp when both are set.
	Accepts func(value string) bool
	// FromHelp takes the legal set from the flag's declared enumeration.
	FromHelp bool
	// Legal names the accepted values for the finding text. Optional when
	// FromHelp supplies them.
	Legal func() []string
	// Why is quoted into the finding, and must justify the source.
	Why string
}

// Coverage is the census mg-9324 exists to produce: which of the value-bearing
// flags the corpus actually writes a value for could be judged, and which could
// not. Unchecked is the honest half — a value-checker silently blind to most
// values is worse than none.
type Coverage struct {
	// Checked and Unchecked are "mg list --status" strings, sorted and deduped.
	Checked   []string
	Unchecked []string
}

// Checker holds the CLI surfaces and the rule registries a check runs with.
type Checker struct {
	Surfaces  map[string]*Surface
	Omissions []OmissionRule
	Values    []ValueRule
}

// valueRule finds the rule covering a flag on a command path, if any.
func (c *Checker) valueRule(path []string, flag string) *ValueRule {
	for i := range c.Values {
		r := &c.Values[i]
		if r.Flag == flag && len(r.Path) == len(path) && pathPrefix(r.Path, path) {
			return r
		}
	}
	return nil
}

// legalValues resolves a rule's accepted set for reporting, preferring what the
// tool itself will say over anything written here.
func (c *Checker) legalValues(r *ValueRule, node *Node) []string {
	if r.Legal != nil {
		if v := r.Legal(); len(v) > 0 {
			return v
		}
	}
	if r.FromHelp && node != nil {
		return node.FlagSpecs[r.Flag].Values
	}
	return nil
}

// accepts asks the rule's source whether a value is legal. The second result is
// false when the source could not answer — a FromHelp rule whose flag has
// stopped declaring an enumeration, which must go unjudged rather than reported.
func (c *Checker) accepts(r *ValueRule, node *Node, value string) (ok, answered bool) {
	if r.Accepts != nil {
		return r.Accepts(value), true
	}
	legal := c.legalValues(r, node)
	if len(legal) == 0 {
		return false, false
	}
	for _, v := range legal {
		if strings.EqualFold(v, value) {
			return true, true
		}
	}
	return false, true
}

// CheckFile runs every rule over one prompt's text.
func (c *Checker) CheckFile(name, text string) []Finding {
	f, _ := c.checkFile(name, text)
	return f
}

// checkFile is CheckFile plus the coverage census, which only a corpus-wide
// caller has any use for.
func (c *Checker) checkFile(name, text string) ([]Finding, Coverage) {
	rendered := Render(text)
	var out []Finding
	var cov Coverage

	abs, omits := extractProseClaims(rendered, c.Surfaces)

	// A command quoted INSIDE a denial is not an instruction to run it — it is
	// the thing being denied, and the denial gets judged on its own terms by
	// R3. Without this, mayor.md's correct "There is no `pogo server restart`"
	// reports the very command it warns you off, and a prompt is penalised for
	// naming what does not exist. Built from every claim, retracted or not:
	// suppression here is about what the sentence is DOING, not whether it is
	// right.
	denied := map[int][]string{}
	for _, a := range abs {
		denied[a.Line] = append(denied[a.Line], a.Sentence)
	}
	insideDenial := func(line int, raw string) bool {
		for _, s := range denied[line] {
			if strings.Contains(s, strings.TrimSpace(raw)) {
				return true
			}
		}
		return false
	}

	// --- R1/R2: what the prompt tells you to RUN ------------------------
	for _, inv := range extractInvocations(codeSpans(rendered), c.Surfaces) {
		if inv.Skipped || insideDenial(inv.Line, inv.Raw) {
			continue
		}
		surf := c.Surfaces[inv.Path[0]]
		if inv.Unresolved != "" {
			out = append(out, Finding{
				File: name, Line: inv.Line, Rule: RuleUnknownSubcommand,
				Subject:  strings.Join(inv.Path, " ") + " " + inv.Unresolved,
				Detail:   fmt.Sprintf("%q is not a subcommand of %q", inv.Unresolved, strings.Join(inv.Path, " ")),
				Evidence: inv.Raw,
			})
			continue
		}
		node, ok := surf.Lookup(inv.Path)
		if !ok {
			continue
		}
		for _, f := range inv.Flags {
			if node.Flags[f] {
				continue
			}
			out = append(out, Finding{
				File: name, Line: inv.Line, Rule: RuleUnknownFlag,
				Subject:  strings.Join(inv.Path, " ") + " --" + f,
				Detail:   fmt.Sprintf("%q accepts no --%s", strings.Join(inv.Path, " "), f),
				Evidence: inv.Raw,
			})
		}

		// --- R5: what the prompt tells you to PASS ----------------------
		for _, fv := range inv.Values {
			if !node.Flags[fv.Flag] {
				continue // already reported as an unknown flag
			}
			subject := strings.Join(inv.Path, " ") + " --" + fv.Flag
			rule := c.valueRule(inv.Path, fv.Flag)
			if rule == nil {
				cov.Unchecked = append(cov.Unchecked, subject)
				continue
			}
			cov.Checked = append(cov.Checked, subject)
			// A placeholder is not a claim about a value. `--status=<status>`
			// and `--status=$STATUS` tell the reader to substitute, and the
			// prompt asserts nothing that could be wrong.
			if fv.Value == "" || isPlaceholder(fv.Value) {
				continue
			}
			ok, answered := c.accepts(rule, node, fv.Value)
			if !answered || ok {
				continue
			}
			detail := fmt.Sprintf("%q refuses --%s=%s", strings.Join(inv.Path, " "), fv.Flag, fv.Value)
			if legal := c.legalValues(rule, node); len(legal) > 0 {
				detail += "; it accepts " + strings.Join(legal, ", ")
			}
			if rule.Why != "" {
				detail += " (" + rule.Why + ")"
			}
			out = append(out, Finding{
				File: name, Line: inv.Line, Rule: RuleUnknownFlagValue,
				Subject: subject, Detail: detail, Evidence: inv.Raw,
			})
		}
	}

	// --- R3: what the prompt tells you does NOT EXIST -------------------
	for _, a := range abs {
		if a.Retracted || len(a.Scope) == 0 {
			continue
		}
		surf := c.Surfaces[a.Scope[0]]
		if surf == nil {
			continue
		}
		if a.Literal != "" {
			if real, what := c.literalExists(surf, a); real {
				out = append(out, Finding{
					File: name, Line: a.Line, Rule: RuleFalseAbsence,
					Subject:  a.Literal,
					Detail:   fmt.Sprintf("the prompt says this does not exist; %s does", what),
					Evidence: a.Sentence,
				})
			}
			continue
		}
		node, ok := surf.Lookup(a.Scope)
		if !ok {
			continue
		}
		for _, stem := range a.Stems {
			hits := node.NamesContaining(stem)
			if len(hits) == 0 {
				continue
			}
			out = append(out, Finding{
				File: name, Line: a.Line, Rule: RuleFalseAbsence,
				Subject: strings.Join(a.Scope, " ") + " " + stem,
				Detail: fmt.Sprintf("the prompt denies a %q affordance on %q, which has %s",
					stem, strings.Join(a.Scope, " "), strings.Join(hits, ", ")),
				Evidence: a.Sentence,
			})
			break
		}
	}

	// --- R4: what the prompt tells you to LEAVE OFF ---------------------
	for _, o := range omits {
		if o.Retracted {
			continue
		}
		for _, rule := range c.Omissions {
			if rule.Flag != o.Flag {
				continue
			}
			if len(o.Scope) > 0 && !pathPrefix(rule.Path, o.Scope) && !pathPrefix(o.Scope, rule.Path) {
				continue
			}
			scopes := o.Types
			if o.CatchAll || len(scopes) == 0 {
				scopes = append([]string{""}, scopes...)
			}
			for _, sc := range scopes {
				if rule.AcceptedWithout(sc) {
					continue
				}
				named := sc
				if named == "" {
					named = "the unnamed default case"
				} else {
					named = fmt.Sprintf("%q", sc)
				}
				out = append(out, Finding{
					File: name, Line: o.Line, Rule: RuleBadOmission,
					Subject: strings.Join(rule.Path, " ") + " --" + rule.Flag,
					Detail: fmt.Sprintf("the prompt says to omit --%s for %s, which is refused: %s",
						rule.Flag, named, rule.Why),
					Evidence: o.Sentence,
				})
				break
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return dedupe(out), cov
}

// merge folds another file's census in.
func (c *Coverage) merge(o Coverage) {
	c.Checked = append(c.Checked, o.Checked...)
	c.Unchecked = append(c.Unchecked, o.Unchecked...)
}

// normalize sorts and dedupes both halves, and drops from Unchecked anything
// that some other invocation proved checkable — the same flag can be written
// with a placeholder in one prompt and a rule-covered value in another, and it
// is covered either way.
func (c *Coverage) normalize() {
	c.Checked = sortUnique(c.Checked)
	checked := map[string]bool{}
	for _, s := range c.Checked {
		checked[s] = true
	}
	var un []string
	for _, s := range sortUnique(c.Unchecked) {
		if !checked[s] {
			un = append(un, s)
		}
	}
	c.Unchecked = un
}

func sortUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// dedupe collapses the same defect reported twice. A sentence can match more
// than one claim shape — "there is no `X` flag" is both a literal denial and a
// noun denial — and one line reported twice reads as two problems.
func dedupe(in []Finding) []Finding {
	seen := map[string]bool{}
	out := in[:0]
	for _, f := range in {
		k := fmt.Sprintf("%s\x00%d\x00%s\x00%s", f.File, f.Line, f.Rule, f.Subject)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// literalExists answers whether a literally-named flag or command path, which
// the prompt says is absent, is in fact present.
func (c *Checker) literalExists(surf *Surface, a AbsenceClaim) (bool, string) {
	lit := strings.TrimSpace(a.Literal)
	if strings.HasPrefix(lit, "--") {
		flag := strings.SplitN(strings.TrimPrefix(lit, "--"), "=", 2)[0]
		node, ok := surf.Lookup(a.Scope)
		if !ok {
			return false, ""
		}
		// EXACT match only. `mg show --body` is truly absent even though
		// `--body-hash` exists, and mg-9fc8 is the incident where believing
		// otherwise stored a usage error as a work item's body.
		if node.Flags[flag] {
			return true, strings.Join(a.Scope, " ")
		}
		return false, ""
	}
	words := strings.Fields(lit)
	if len(words) < 2 || c.Surfaces[words[0]] == nil {
		return false, ""
	}
	if _, ok := c.Surfaces[words[0]].Lookup(words); ok {
		return true, "the command tree"
	}
	return false, ""
}

func pathPrefix(short, long []string) bool {
	if len(short) > len(long) {
		return false
	}
	for i := range short {
		if short[i] != long[i] {
			return false
		}
	}
	return true
}

// Report is what a corpus check produces: the findings, and the coverage census
// saying which value-bearing flags it was able to judge.
//
// Coverage is not decoration. mg-9324's mandate is that the UNCOVERED count is
// the real output, because a value-checker blind to most values converts
// "unchecked" into "checked and fine" — the failure mode of this whole ticket
// family. Every caller that prints findings must print the census too.
type Report struct {
	Files    int
	Findings []Finding
	Coverage Coverage
}

// CheckFS runs the check over every .md file in a prompt corpus.
func CheckFS(c *Checker, root fs.FS) (Report, error) {
	var rep Report
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		rep.Files++
		findings, cov := c.checkFile(path, string(data))
		rep.Findings = append(rep.Findings, findings...)
		rep.Coverage.merge(cov)
		return nil
	})
	out := rep.Findings
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	rep.Coverage.normalize()
	return rep, err
}
