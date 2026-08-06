package agent

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// TestShippedPromptsNameNoPersonalFleetAgent walks the embedded prompt corpus —
// every file InstallPrompts writes into a consumer's ~/.pogo/agents — and fails
// on a reference to an agent, repo, or filesystem path that exists on one
// particular machine.
//
// The defect this locks out is not "a name appears in a file". It is a prompt
// that TELLS A FRESH INSTALL TO ACT on a name only the author's fleet has
// (mg-f04b). polecat-triage.md carried the worst shape of it: `mg mail send
// pm-pogo`, followed by an instruction to wait up to two hours for the reply and
// not to finalize without one. On any machine but the author's, `pm-pogo` is not
// an agent — and mg files mail for an unrecognized recipient into a fresh
// maildir instead of refusing, so the send succeeded, the reply never came, and
// the workflow stalled with every instrument reporting a delivered message.
//
// Scope note: this checks the SHIPPED corpus only. `~/.pogo/agents/crew/*.md` is
// the operator's own material and is none of this test's business — the split is
// the one ARCHITECTURE.md draws between install output and local prompts.
func TestShippedPromptsNameNoPersonalFleetAgent(t *testing.T) {
	// Each pattern is a name that belongs to one deployment. `pm-<anything>` is
	// matched as a class rather than by roster: the point is that NO named PM
	// instance is a shipped assumption, whether or not it is one we have seen.
	banned := []struct {
		re   *regexp.Regexp
		what string
		// ok lists matches that are not instance names at all: `pm-template`
		// is the shipped baseline file every PM extends, and the corpus has to
		// be able to say its own filename.
		ok map[string]bool
	}{
		{
			re:   regexp.MustCompile(`\bpm-[a-z0-9]+\b`),
			what: `a named PM instance (use {{.SME}} / [agents] sme, or write it as pm-<project>)`,
			ok:   map[string]bool{"pm-template": true},
		},
		{
			re:   regexp.MustCompile(`/Users/[a-z]+/`),
			what: "an absolute path under a particular user's home",
		},
		{
			re:   regexp.MustCompile(`(?i)\b(onethird|one_third|dealdesk|lineara)\b`),
			what: "a project that exists on one machine",
		},
	}
	// There is deliberately NO exemption list. Prompts that genuinely need to
	// show a PM name write it as `pm-<project>`, which is visibly a placeholder
	// and cannot be read as an address — that keeps the rule "no shipped prompt
	// names a real PM instance" absolute rather than a judgement call per line,
	// and a judgement call per line is exactly what let `pm-pogo` accumulate to
	// fifteen sites in the first place.
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		body, err := fs.ReadFile(DefaultPromptsFS(), path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(body), "\n") {
			for _, b := range banned {
				for _, hit := range b.re.FindAllString(line, -1) {
					if b.ok[hit] {
						continue
					}
					t.Errorf("%s: shipped prompt names %s (%q):\n  %s",
						path, b.what, hit, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk prompt corpus: %v", err)
	}
}

// TestTriageConsultOmittedWithoutSME renders polecat-triage.md both ways and
// checks the halves that matter: with no SME configured the prompt must contain
// no mail-send step at all, and with one it must address that exact name.
//
// Rendering rather than string-matching the source is the point. `{{if .SME}}`
// reads correct in either direction; only expansion shows whether an empty SME
// leaves behind a `mg mail send` with a blank recipient — a command that fails
// at the shell and would land on the worker as an unexplained error rather than
// as the "there is no SME here" it actually means.
func TestTriageConsultOmittedWithoutSME(t *testing.T) {
	body, err := fs.ReadFile(DefaultPromptsFS(), "templates/polecat-triage.md")
	if err != nil {
		t.Fatalf("read polecat-triage.md: %v", err)
	}
	tmpl, err := template.New("triage").Parse(string(body))
	if err != nil {
		t.Fatalf("parse polecat-triage.md: %v", err)
	}

	render := func(sme string) string {
		var sb strings.Builder
		if err := tmpl.Execute(&sb, withDefaults(TemplateVars{Id: "mg-0000", SME: sme})); err != nil {
			t.Fatalf("execute with SME=%q: %v", sme, err)
		}
		return sb.String()
	}

	// SetSMEName is process-wide; withDefaults reads it when the field is
	// empty. Pin it to empty so the no-SME case is the one being rendered even
	// if another test in this package configured one.
	t.Cleanup(func() { SetSMEName("") })
	SetSMEName("")

	without := render("")
	if strings.Contains(without, "mg mail send \n") || strings.Contains(without, "mg mail send  ") {
		t.Error("polecat-triage.md with no SME: rendered a mail-send with an empty recipient")
	}
	if strings.Contains(without, "triage consult:") {
		t.Error("polecat-triage.md with no SME: consult step survived into the rendered prompt")
	}
	// The packet field is a `<...>` placeholder like every other field in that
	// block — the raw template has to stay parseable JSON, because the mayor's
	// transition-3 script lifts the block with awk and pipes it through `jq -e`
	// before `mg done --result` sees it. A `{{if}}` inside the JSON literal
	// breaks that pipeline in the raw file, where it is never expanded.
	if !strings.Contains(without, `"sme_consulted"`) {
		t.Error("polecat-triage.md with no SME: packet must still carry sme_consulted, " +
			"so a skipped consult is on the record rather than absent from it")
	}
	if !strings.Contains(without, "\"sme_consulted\": false") {
		t.Error("polecat-triage.md with no SME: the else branch must tell the worker to report false")
	}

	with := render("pm-example")
	if !strings.Contains(with, "mg mail send pm-example ") {
		t.Error("polecat-triage.md with an SME: expected the consult addressed to the configured name")
	}
	if !strings.Contains(with, `"sme_consulted"`) {
		t.Error("polecat-triage.md with an SME: packet must carry sme_consulted")
	}
}

// TestSMENameHasNoDefault pins the one property that makes the SME seam safe:
// unconfigured resolves to the empty string, never to a name.
//
// A fallback here would be worse than no feature. The consult is a mail send,
// and a mail sent to a wrong-but-plausible name is delivered, filed, and never
// read — indistinguishable at every instrument from one that was answered.
func TestSMENameHasNoDefault(t *testing.T) {
	t.Cleanup(func() { SetSMEName("") })

	SetSMEName("")
	if got := SMEName(); got != "" {
		t.Errorf("SMEName() with nothing configured = %q, want \"\"", got)
	}

	SetSMEName("pm-example")
	if got := SMEName(); got != "pm-example" {
		t.Errorf("SMEName() = %q, want the configured name", got)
	}

	// And the placeholder must expand to nothing, not to the literal token, so
	// a static prompt that used it does not ship a raw `{{.SME}}` to a reader.
	SetSMEName("")
	if got := substituteRoleNames("ask " + smePlaceholder + " about it"); strings.Contains(got, "{{") {
		t.Errorf("substituteRoleNames left an unexpanded placeholder: %q", got)
	}
}
