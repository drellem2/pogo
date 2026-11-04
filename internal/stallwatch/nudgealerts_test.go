package stallwatch

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/alerts"
)

// categoryConsts reads every `category<X> = "..."` constant in this package's
// non-test source, so a new check is covered without editing this test.
func categoryConsts(t *testing.T) map[string]string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, s := range g.Specs {
					vs := s.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if !strings.HasPrefix(n.Name, "category") || i >= len(vs.Values) {
							continue
						}
						if b, ok := vs.Values[i].(*ast.BasicLit); ok && b.Kind == token.STRING {
							v, _ := strconv.Unquote(b.Value)
							out[n.Name] = v
						}
					}
				}
			}
		}
	}
	return out
}

// drellem2/pogo#262 slice 4: every stall-watch check's nudge is a declared
// alert, so every one can be muted and routed. A category deliver does not
// map would be refused, not sent unnamed.
func TestEveryCategoryIsAnAlert(t *testing.T) {
	cats := categoryConsts(t)
	if len(cats) < 8 {
		t.Fatalf("found %d category constants, want at least 8 — the parse is not reading the package", len(cats))
	}
	for ident, cat := range cats {
		w, rec, _, _ := testEnv(t, blockedCfg())
		_, to, muted, err := w.deliver(cat, "mayor", Notice{Subject: "s"})
		if err != nil || muted != "" || to != "mayor" || rec.nudgeCount() != 1 {
			t.Errorf("%s (%q): err=%v muted=%q to=%q nudges=%d — want one nudge to mayor", ident, cat, err, muted, to, rec.nudgeCount())
		}
		name := alerts.Name("stall_watch." + cat)
		d, ok := alerts.Lookup(name)
		if !ok || d.Channel() != alerts.ChannelNudge {
			t.Errorf("%s: %q is not a declared nudge alert (declared=%v)", ident, name, ok)
		}
	}
	// Control: an unknown category is refused rather than nudged.
	w, rec, _, _ := testEnv(t, blockedCfg())
	if _, _, _, err := w.deliver("no_such_check", "mayor", Notice{}); err == nil || rec.nudgeCount() != 0 {
		t.Errorf("unknown category: err=%v nudges=%d, want a refusal and no nudge", err, rec.nudgeCount())
	}
}

func TestStallNudgeMutedPerCategory(t *testing.T) {
	alerts.SetMutes(map[alerts.Name]string{alerts.StallWatchUnreadMail: `[alerts."stall_watch.unread_mail"] mute = true`})
	t.Cleanup(func() { alerts.SetMutes(nil) })
	w, rec, _, _ := testEnv(t, blockedCfg())
	w.fire(categoryUnreadMail, Notice{Subject: "unread"}, map[string]any{"category": categoryUnreadMail})
	if rec.nudgeCount() != 0 {
		t.Fatalf("muted unread_mail nudged %d time(s)", rec.nudgeCount())
	}
	if len(rec.events) != 1 || rec.events[0].Details["nudge_delivery"] != "muted" {
		t.Fatalf("events %+v, want one stall_watch_fired marked muted", rec.events)
	}
	// Another category is untouched by that mute.
	w.fire(categoryUnclaimedItems, Notice{Subject: "unclaimed"}, map[string]any{"category": categoryUnclaimedItems})
	if rec.nudgeCount() != 1 {
		t.Errorf("unclaimed_items nudges = %d, want 1 — a mute of one category reached another", rec.nudgeCount())
	}
}

func TestStallNudgeRouted(t *testing.T) {
	alerts.SetRoutes(map[alerts.Name]alerts.Route{
		alerts.StallWatchStrandedPush: {To: "pm-pogo", Match: "mayor", Fallback: "mayor", Source: "test"},
	})
	t.Cleanup(func() { alerts.SetRoutes(nil) })
	w, rec, _, _ := testEnv(t, blockedCfg())
	w.fire(categoryStrandedPush, Notice{Subject: "pushed"}, map[string]any{"category": categoryStrandedPush})
	if len(rec.nudges) != 1 || rec.nudges[0].agent != "pm-pogo" {
		t.Fatalf("nudges %+v, want one to pm-pogo", rec.nudges)
	}
	if got := rec.events[0].Details["nudge_routed_to"]; got != "pm-pogo" {
		t.Errorf("nudge_routed_to = %v, want pm-pogo", got)
	}

	// A routed nudge that fails falls back to the default recipient.
	rec2 := &recorder{}
	w.nudge = func(agent string, n Notice) (Delivery, error) {
		if agent == "pm-pogo" {
			return Delivery{}, errors.New("not running and no mailbox")
		}
		return rec2.nudge(agent, n)
	}
	_, to, _, err := w.deliver(categoryStrandedPush, "mayor", Notice{Subject: "pushed"})
	if err != nil || to != "mayor" || len(rec2.nudges) != 1 || rec2.nudges[0].agent != "mayor" {
		t.Errorf("fallback: err=%v to=%q nudges=%+v, want one nudge to mayor", err, to, rec2.nudges)
	}

	// A nudge addressed to someone other than the route's Match is untouched.
	rec3 := &recorder{}
	w.nudge = rec3.nudge
	if _, to, _, _ := w.deliver(categoryStrandedPush, "other", Notice{}); to != "other" || len(rec3.nudges) != 1 {
		t.Errorf("unmatched recipient rerouted: to=%q nudges=%+v", to, rec3.nudges)
	}
}
