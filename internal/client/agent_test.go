package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// withTestServer points the package-level serverURL at a test handler for
// the duration of the test, restoring it on cleanup. The handler may return
// any status / body / Content-Type to simulate different pogod responses.
func withTestServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	old := serverURL
	serverURL = ts.URL
	t.Cleanup(func() {
		serverURL = old
		ts.Close()
	})
}

// TestStartAgent_PromptNotFoundStructured covers the GitHub Issue #15 /
// mg-be51 fix: when pogod returns a structured 404 because the prompt file
// is missing, the CLI surfaces the actionable message verbatim instead of
// telling the user to rebuild pogod.
func TestStartAgent_PromptNotFoundStructured(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(agent.StartErrorResponse{
			Reason:  "prompt-not-found",
			Path:    "/home/user/.pogo/agents/crew/foo.md",
			Message: "prompt file not found: /home/user/.pogo/agents/crew/foo.md (run 'pogo agent prompt install' to install defaults)",
		})
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prompt file not found") {
		t.Errorf("expected message to name the missing prompt, got: %v", err)
	}
	if !strings.Contains(msg, "pogo agent prompt install") {
		t.Errorf("expected message to include the fix command, got: %v", err)
	}
	if strings.Contains(msg, "rebuild") || strings.Contains(msg, "restart pogod") {
		t.Errorf("must NOT suggest rebuilding pogod for a missing prompt, got: %v", err)
	}
}

// TestStartAgent_PromptNotFoundPlainText covers the backwards-compat path:
// an older pogod returns a plain-text 404 body via http.Error. The CLI must
// still surface the body verbatim rather than blaming the build.
func TestStartAgent_PromptNotFoundPlainText(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "prompt file not found: /home/user/.pogo/agents/crew/foo.md (run 'pogo agent prompt install' to install defaults)", http.StatusNotFound)
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prompt file not found") {
		t.Errorf("expected message to surface plain-text body, got: %v", err)
	}
	if strings.Contains(msg, "rebuild") || strings.Contains(msg, "restart pogod") {
		t.Errorf("must NOT suggest rebuilding pogod for a missing prompt, got: %v", err)
	}
}

// TestStartAgent_EndpointMissing covers the legitimate "rebuild pogod"
// path — a 404 from a daemon that doesn't know /agents/start (Go's default
// ServeMux 404 body).
func TestStartAgent_EndpointMissing(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "restart pogod") {
		t.Errorf("expected rebuild-pogod message for default 404, got: %v", err)
	}
}

// TestStartAgent_GreetingsSentinel covers the other rebuild-pogod path: a
// stale pogod (or a different process on the port) whose root handler
// answers with "greetings from pogo daemon".
func TestStartAgent_GreetingsSentinel(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("greetings from pogo daemon"))
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "restart pogod") {
		t.Errorf("expected rebuild-pogod message for greetings sentinel, got: %v", err)
	}
}

// TestSpawnAgent_EndpointMissing and TestSpawnPolecat_EndpointMissing
// confirm the rebuild-pogod branch is reachable through all three call
// sites, not just StartAgent.
func TestSpawnAgent_EndpointMissing(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, err := SpawnAgent(agent.SpawnAPIRequest{Name: "x", Type: agent.TypePolecat, Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "restart pogod") {
		t.Fatalf("expected rebuild-pogod error, got: %v", err)
	}
}

func TestSpawnPolecat_EndpointMissing(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "restart pogod") {
		t.Fatalf("expected rebuild-pogod error, got: %v", err)
	}
}

// TestSpawnPolecat_TemplateNotFound is the symmetric case for SpawnPolecat:
// when handleSpawnPolecat returns a real 404 with a meaningful body
// ("template foo not found"), the CLI surfaces it rather than blaming the
// build.
func TestSpawnPolecat_TemplateNotFound(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "template \"missing\" not found", http.StatusNotFound)
	})
	_, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x", Template: "missing"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "template") {
		t.Errorf("expected template message to surface, got: %v", err)
	}
	if strings.Contains(err.Error(), "restart pogod") {
		t.Errorf("must NOT suggest rebuilding pogod for a meaningful 404 body, got: %v", err)
	}
}

// TestCompleteMGWorkItem_BuildsMGDoneCommand verifies the mg done invocation
// pogod's OnMerged reap issues on a merged polecat's behalf (gh #35):
// `mg done <id> --result=<json>`, with the --result flag omitted when no
// sidecar is given.
func TestCompleteMGWorkItem_BuildsMGDoneCommand(t *testing.T) {
	var got []string
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		// Run a no-op so CombinedOutput succeeds.
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = old })

	if err := CompleteMGWorkItem("mg-1234", `{"branch":"polecat-mg-1234"}`); err != nil {
		t.Fatalf("CompleteMGWorkItem: %v", err)
	}
	want := []string{"mg", "done", "mg-1234", `--result={"branch":"polecat-mg-1234"}`}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("command = %v, want %v", got, want)
	}

	if err := CompleteMGWorkItem("mg-1234", ""); err != nil {
		t.Fatalf("CompleteMGWorkItem (no result): %v", err)
	}
	want = []string{"mg", "done", "mg-1234"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("command (no result) = %v, want %v", got, want)
	}
}

// fakeMGShow makes `mg show <id> --json` print out, or fail when out is empty.
func fakeMGShow(t *testing.T, out string) {
	t.Helper()
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if out == "" {
			return exec.Command("false")
		}
		return exec.Command("printf", "%s", out)
	}
	t.Cleanup(func() { execCommand = old })
}

// TestMGWorkItemDeclaresPostMergeWork reads the marker that tells pogod a merge
// is a STEP rather than completion (mg-d86e). The declaring fixture is the tag
// list mg-ca3c would have carried: a real release ticket whose merge was
// treated as completion, so the tag it still owed was never pushed.
func TestMGWorkItemDeclaresPostMergeWork(t *testing.T) {
	cases := []struct {
		name string
		tags string
		want bool
	}{
		{"declared", `["pogo","release","post-merge-work"]`, true},
		{"undeclared", `["pogo","bug"]`, false},
		{"no tags at all", `[]`, false},
		// Case and stray whitespace resolve TOWARD the declaration: being
		// generous can at worst leave an item for its polecat to complete,
		// while being stingy truncates the ticket silently.
		{"case and space folded", `["  Post-Merge-Work "]`, true},
		// The neighbouring declaration must not be mistaken for this one:
		// declares-remainder says something ELSE must carry the work forward.
		{"declares-remainder is a different property", `["declares-remainder"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeMGShow(t, `{"id":"mg-ca3c","status":"claimed","tags":`+tc.tags+`}`)
			got, err := MGWorkItemDeclaresPostMergeWork("mg-ca3c")
			if err != nil {
				t.Fatalf("MGWorkItemDeclaresPostMergeWork: %v", err)
			}
			if got != tc.want {
				t.Errorf("tags %s -> %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
}

// TestMGWorkItemDeclaresPostMergeWork_ErrorsAreNotFalse keeps "I could not read
// the item" distinct from "the item declares nothing". Collapsing them would
// hand pogod a confident no on exactly the lookups that failed, which is the
// mg-d86e failure shape one layer down.
func TestMGWorkItemDeclaresPostMergeWork_ErrorsAreNotFalse(t *testing.T) {
	fakeMGShow(t, "")
	if _, err := MGWorkItemDeclaresPostMergeWork("mg-ca3c"); err == nil {
		t.Error("a failing mg show must return an error, not a confident false")
	}

	fakeMGShow(t, "not json at all")
	if _, err := MGWorkItemDeclaresPostMergeWork("mg-ca3c"); err == nil {
		t.Error("unparseable output must return an error, not a confident false")
	}

	if _, err := MGWorkItemDeclaresPostMergeWork(""); err == nil {
		t.Error("an empty work-item id must be an error")
	}
}

// fakeMGShowStderr makes `mg show <id> --json` print stdout to STDOUT and noise
// to STDERR, both on a successful (exit 0) run. That is the real shape of
// `mg show` for an id that also names an archived item, and it is the shape a
// CombinedOutput-based probe cannot survive.
func fakeMGShowStderr(t *testing.T, stdout, stderr string) {
	t.Helper()
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", `printf '%s' "$1" >&2; printf '%s' "$2"`, "sh", stderr, stdout)
	}
	t.Cleanup(func() { execCommand = old })
}

// TestMGShowProbesReadStdoutOnly is the regression control for the round-1
// blocking finding on PR #133.
//
// `mg show <id> --json` exits 0 with valid JSON on stdout while ALSO writing an
// advisory to stderr whenever a live id happens to name an archived item too:
//
//	note: mg-4b2a also names an archived item at work/archive/2026-04/mg-4b2a.md
//
// Under `CombinedOutput` that advisory is prepended to the JSON, the unmarshal
// fails, and every probe here reports "cannot tell" for an item the store
// answered perfectly. Measured on the live store: 4 of 545 live items collide
// today, and against 2,176 archived ids in a 4-hex-digit space roughly 3% of
// newly-filed items will.
//
// For MGWorkItemReviews the cost is that the mg-aaf6 exemption never fires on
// ANY tick for an affected review ticket — the builder is reaped mid-review and
// the obvious diagnosis ("the coordinator forgot the `reviews:` line") points
// away from the cause. All three probes are pinned, not just the new one: they
// share a code path and are consulted by the same reaper on the same tick.
func TestMGShowProbesReadStdoutOnly(t *testing.T) {
	const noise = "note: mg-4b2a also names an archived item at work/archive/2026-04/mg-4b2a.md\n"
	body, err := json.Marshal("\n# review: x\nworkflow: gh-issue\nstage: review\nreviews: mg-aaf6\n")
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"id":"mg-4b2a","status":"done","tags":["post-merge-work"],"body":` + string(body) + `}`

	t.Run("MGWorkItemReviews", func(t *testing.T) {
		fakeMGShowStderr(t, payload, noise)
		got, err := MGWorkItemReviews("mg-4b2a")
		if err != nil {
			t.Fatalf("stderr noise broke the probe: %v — the store answered with valid JSON on stdout", err)
		}
		if got != "mg-aaf6" {
			t.Errorf("Reviews = %q, want mg-aaf6", got)
		}
	})

	t.Run("MGWorkItemDone", func(t *testing.T) {
		fakeMGShowStderr(t, payload, noise)
		got, err := MGWorkItemDone("mg-4b2a")
		if err != nil {
			t.Fatalf("stderr noise broke the probe: %v", err)
		}
		if !got {
			t.Error("status done was not read through the stderr noise")
		}
	})

	t.Run("MGWorkItemDeclaresPostMergeWork", func(t *testing.T) {
		fakeMGShowStderr(t, payload, noise)
		got, err := MGWorkItemDeclaresPostMergeWork("mg-4b2a")
		if err != nil {
			t.Fatalf("stderr noise broke the probe: %v", err)
		}
		if !got {
			t.Error("the post-merge-work tag was not read through the stderr noise")
		}
	})
}

// TestMGShowProbeErrorStillNamesWhatMgSaid — reading stdout only must not cost
// the diagnosis on a genuine failure. mg writes its errors to stderr, so an
// error that dropped stderr would report an empty reason.
func TestMGShowProbeErrorStillNamesWhatMgSaid(t *testing.T) {
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", `printf 'no such work item: mg-zzzz' >&2; exit 3`)
	}
	t.Cleanup(func() { execCommand = old })

	_, err := MGWorkItemReviews("mg-zzzz")
	if err == nil {
		t.Fatal("a failing `mg show` must be an error")
	}
	if !strings.Contains(err.Error(), "no such work item") {
		t.Errorf("the error dropped what mg actually said: %v", err)
	}
}

// TestMGWorkItemReviews reads the `reviews:` carrier line pogod's done-reaper
// uses to keep a builder alive while its reviewer is running (mg-aaf6, gh#131).
//
// The fixtures are `mg show --json` payloads: the field this reads is `.body`,
// and the parse it applies to that body is workitem.ParseCarrier — the same
// parser the dispatch gate applies to the file on disk.
func TestMGWorkItemReviews(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "the shipped review-ticket shape",
			body: `\n# review: gh#131 part 3\nworkflow: gh-issue\nstage: review\ngh: drellem2/pogo#131\nreviews: mg-aaf6\n\nReview the PR.\n`,
			want: "mg-aaf6",
		},
		{
			name: "a build ticket declares no review",
			body: `\n# build: gh#131 part 3\nworkflow: gh-issue\nstage: build\ngh: drellem2/pogo#131\n\nBuild it.\n`,
			want: "",
		},
		{
			name: "an ordinary item with no carrier at all",
			body: `\n# fix the thing\n\nIt is broken.\n`,
			want: "",
		},
		{
			// A body DISCUSSING the convention is not declaring one — and this is
			// the routine case in this feature's own tree, where the triage and
			// build tickets both write the line in prose while explaining it.
			name: "prose that mentions the line is not a declaration",
			body: `\n# triage: builders strand reviewers\nworkflow: gh-issue\nstage: triage\n\nThe adopted shape is a carrier line:\n\nreviews: mg-aaf6\n\nwritten once at creation.\n`,
			want: "",
		},
		{
			// An unreachable block is "cannot tell", never "declares nothing".
			// Collapsing the two is how a declaration that is plainly visible in
			// `mg show` silently fails to protect anything (mg-27d4).
			name:    "an out-of-reach carrier block is an error, not an absence",
			body:    `\n# review: gh#131 part 3\n\nPR: https://github.com/drellem2/pogo/pull/999\n\nworkflow: gh-issue\nstage: review\nreviews: mg-aaf6\n`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(strings.ReplaceAll(tc.body, `\n`, "\n"))
			if err != nil {
				t.Fatal(err)
			}
			fakeMGShow(t, `{"id":"mg-1c60","status":"claimed","body":`+string(body)+`}`)
			got, err := MGWorkItemReviews("mg-1c60")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error for %s, got %q — an unreadable declaration must not read as an absent one", tc.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("MGWorkItemReviews: %v", err)
			}
			if got != tc.want {
				t.Errorf("MGWorkItemReviews = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMGWorkItemReviewsFailureIsAnError — a store that will not answer, or an
// empty id, must return an error rather than "". The caller treats "" as "this
// item declares no review" and reaps the builder, so a swallowed failure is a
// builder reaped mid-review with nothing in the log to say why.
func TestMGWorkItemReviewsFailureIsAnError(t *testing.T) {
	if _, err := MGWorkItemReviews(""); err == nil {
		t.Error("an empty id must be an error, not an empty declaration")
	}
	fakeMGShow(t, "") // makes the command fail
	if _, err := MGWorkItemReviews("mg-1c60"); err == nil {
		t.Error("a failing `mg show` must be an error, not an empty declaration")
	}
	fakeMGShow(t, "not json")
	if _, err := MGWorkItemReviews("mg-1c60"); err == nil {
		t.Error("unparseable JSON must be an error, not an empty declaration")
	}
}
