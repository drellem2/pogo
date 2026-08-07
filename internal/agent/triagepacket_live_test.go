package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mg-1912: polecat-triage.md told the triage worker to record its recommendation
// packet with `mg done --result` on a ticket that CANNOT complete at that point.
// The triage ticket's body leads with `stage: triage`, so mg files it carrying
// `declares-remainder`; `mg done` then refuses a declared item that names no
// successor, and the successor is the build ticket, which is not filed until
// after the human gate. workitem.Done ran that guard BEFORE it wrote the
// sidecar, so the refusal discarded the packet the playbook calls the record of
// record. There was no ordering of the playbook in which the line worked.
//
// The fix moves WHERE the packet lands: the worker appends it to the triage
// ticket's own body as a fenced `json triage-packet` block, which has no
// successor precondition, and the coordinator lifts that block into
// `--result` at transition 3 when it retires the ticket with `--successor`.
//
// mg-9259 has since fixed the OTHER half, upstream in mg: the result is written
// before the guards run, so a refused `mg done` now preserves the payload rather
// than destroying it. That does not make the relocation above redundant — the
// refusal still fires, so `mg done --result` is still not a way to REPORT at
// triage time, and the body block is still what a human reads at the gate. It
// does mean this file no longer asserts that a refusal costs the packet. It
// asserts the opposite, deliberately; see TestRefusedDoneKeepsTheResultItWasGiven.
//
// These tests run the SHIPPED shell out of the SHIPPED templates against a real
// `mg` and a real store, rather than restating what the templates ought to say.
// The blocks are extracted, not transcribed, so prompt and test cannot drift:
// reword the commands and this file runs the new wording.

// packetFence is the marker both sides of the handoff agree on. It is asserted
// against both templates rather than assumed, because it is the whole contract
// between the worker that writes the block and the coordinator that reads it.
const packetFence = "```json triage-packet"

// mgStore stands up a private macguffin store and pins $MG_ROOT at it. The root
// is carved out of the package sandbox (see testmain_test.go), so it is already
// vetted as not resolving onto the developer's live ~/.macguffin — which matters
// more here than in most tests, since every command below is the real `mg`.
func mgStore(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("mg"); err != nil {
		t.Skip("mg is not on PATH")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH")
	}
	root := filepath.Join(sandbox.Root, "triagepacket", t.Name(), "macguffin")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir store root: %v", err)
	}
	if !sandbox.Contains(root) {
		t.Fatalf("store root %s is outside the sandbox %s", root, sandbox.Root)
	}
	t.Setenv("MG_ROOT", root)
	if out, err := exec.Command("mg", "init").CombinedOutput(); err != nil {
		t.Fatalf("mg init: %v\n%s", err, out)
	}
	return root
}

// sh runs a shell fragment with the pinned $MG_ROOT and returns its combined
// output and exit code. Nonzero is a normal outcome here — two of the three
// controls below are refusals.
func sh(t *testing.T, script string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	return string(out), code
}

var mgIDRe = regexp.MustCompile(`mg-[0-9a-f]{4,}`)

// fileItem creates a work item and returns its id. bodyLead is prepended to the
// body verbatim, which is how the `declares-remainder` carrier is triggered: mg
// emits the tag from a body leading with `stage: triage` on ANY type, task
// included, because a triage is a workflow position rather than a type.
func fileItem(t *testing.T, title, body string) string {
	t.Helper()
	cmd := exec.Command("mg", "new", "--type=task", "--no-repo", "--title="+title, "--body="+body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mg new %q: %v\n%s", title, err, out)
	}
	id := mgIDRe.FindString(string(out))
	if id == "" {
		t.Fatalf("mg new %q: no work item id in output:\n%s", title, out)
	}
	return id
}

func mgField(t *testing.T, id, jqPath string) string {
	t.Helper()
	out, code := sh(t, "mg show "+id+" --json | jq -r '"+jqPath+"'")
	if code != 0 {
		t.Fatalf("mg show %s | jq %s: exit %d\n%s", id, jqPath, code, out)
	}
	return strings.TrimSpace(out)
}

// resultSidecars returns every <id>.result.json anywhere in the store. The glob
// is deliberately store-wide rather than status-scoped because WHERE the sidecar
// sits is itself the assertion (mg-9259): a result is a companion to its item and
// travels with it, so it is expected beside a claimed item and beside a done one,
// and "somewhere else entirely" is the failure. A done/-only check could not tell
// a packet preserved in claimed/ from a packet destroyed.
func resultSidecars(t *testing.T, root, id string) []string {
	t.Helper()
	hits, err := filepath.Glob(filepath.Join(root, "work", "*", id+".result.json"))
	if err != nil {
		t.Fatalf("glob sidecars: %v", err)
	}
	return hits
}

// shippedBashBlock pulls the four-backtick bash block containing needle out of a
// shipped prompt and dedents it so the heredocs inside it terminate. Four
// backticks are how these blocks are fenced, precisely because their contents
// include three-backtick fences of their own.
func shippedBashBlock(t *testing.T, prompt, needle string) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile(prompt)
	if err != nil {
		t.Fatalf("read %s: %v", prompt, err)
	}
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "````bash" {
			continue
		}
		indent := ln[:strings.Index(ln, "`")]
		var body []string
		for _, in := range lines[i+1:] {
			if strings.TrimSpace(in) == "````" {
				break
			}
			body = append(body, strings.TrimPrefix(in, indent))
		}
		block := strings.Join(body, "\n")
		if strings.Contains(block, needle) {
			return block
		}
	}
	t.Fatalf("%s: found no ````bash block containing %q", prompt, needle)
	return ""
}

// TestTriagePacketIsWrittenBeforeAnySuccessorExists is mg-1912's acceptance
// control, and it is deliberately run on the arm the old instruction could not
// handle: NO successor exists anywhere in the store when the packet is written.
// A fix verified only on the has-successor path has not been tested against the
// defect at all, since the has-successor path is the one that always worked.
func TestTriagePacketIsWrittenBeforeAnySuccessorExists(t *testing.T) {
	mgStore(t)

	triage := fileItem(t, "triage: probe (o/r#1)",
		"workflow: gh-issue\nstage: triage\ngh: o/r#1\n\nTriage this GitHub issue.")

	// The carrier block did fire. Without this the rest of the test would be
	// exercising an ordinary task, and would pass while proving nothing.
	if tags := mgField(t, triage, ".tags|join(\",\")"); !strings.Contains(tags, "declares-remainder") {
		t.Fatalf("triage ticket tags = %q, want declares-remainder; the `stage: triage` "+
			"carrier block did not fire, so this fixture is not the defect's shape", tags)
	}
	if out, code := sh(t, "mg claim "+triage); code != 0 {
		t.Fatalf("mg claim: exit %d\n%s", code, out)
	}

	// ---- the premise, measured -----------------------------------------------
	// The instruction as it shipped still cannot succeed: `mg done` refuses a
	// declared item that names no successor, and at this point on the gh-issue
	// track no id exists that could satisfy it. That refusal is the entire reason
	// the packet was relocated to the ticket body, so if it ever stops firing the
	// relocation needs re-arguing rather than silently outliving its cause.
	//
	// What the refusal COSTS is no longer part of this test. It used to also
	// assert that the refusal wrote no sidecar; mg-9259 made a refused `mg done`
	// preserve the payload, and that guarantee now has its own control in
	// TestRefusedDoneKeepsTheResultItWasGiven. The two are separate claims from
	// separate tickets: this one is mg-1912's (the body route works with no
	// successor in the store), that one is mg-9259's (a refusal costs a retry,
	// never the work).
	out, code := sh(t, `mg done `+triage+` --result='{"workflow":"gh-issue","stage":"triage"}'`)
	if code == 0 {
		t.Fatalf("mg done --result succeeded on a declared item with no successor; "+
			"this test's premise is gone and the template guidance needs rereading:\n%s", out)
	}

	// ---- the fix, on the no-successor arm ------------------------------------
	// Run the template's own step-8 block. Nothing has been filed that could
	// serve as a successor, and nothing needs to be.
	block := shippedBashBlock(t, "prompts/templates/polecat-triage.md", "--append-body-file")
	if !strings.Contains(block, packetFence) {
		t.Fatalf("polecat-triage.md step 8 does not write the %q fence; the coordinator's "+
			"extractor keys on it:\n%s", packetFence, block)
	}
	scratch := t.TempDir()
	script := strings.NewReplacer("{{.Id}}", triage, "/tmp/", scratch+"/").Replace(block)
	if out, code := sh(t, script); code != 0 {
		t.Fatalf("the shipped step-8 block failed: exit %d\n%s\n--- script ---\n%s", code, out, script)
	}

	// The packet is on the record, and the record is the work item — reachable
	// by id by anyone, after this worker is gone and its worktree reaped.
	body := mgField(t, triage, ".body")
	if !strings.Contains(body, packetFence) {
		t.Fatalf("no %q block on the triage ticket body after step 8:\n%s", packetFence, body)
	}

	// The extractor the coordinator is told to use, taken from mayor.md, gets
	// machine-readable JSON back out. Round-tripping through a real `mg show`
	// is the point: the block has to survive being stored and re-read.
	mayor := shippedBashBlock(t, "prompts/mayor.md", "PACKET=$(mg show")
	extract := strings.Replace(mayor, "<triage ticket id>", triage, -1)
	extract = extract[:strings.Index(extract, "\nmg done ")] + "\nprintf '%s' \"$PACKET\""
	packet, code := sh(t, extract)
	if code != 0 {
		t.Fatalf("mayor.md's extractor failed: exit %d\n%s", code, packet)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(packet), &decoded); err != nil {
		t.Fatalf("extracted packet is not a JSON object: %v\ngot:\n%s", err, packet)
	}
	for _, key := range []string{"workflow", "stage", "issue", "kind", "recommendation",
		"summary", "effort", "checked", "reproduced", "remainder", "proposed_public_reply"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("extracted packet is missing the %q key", key)
		}
	}

	// ---- and the declaration is still visible, not smoothed over -------------
	// The ticket owes something and nothing tracks it yet. That state is the
	// honest one, and it stays queryable: still claimed, still declared, no
	// successor tag invented to make a command pass.
	if got := mgField(t, triage, ".status"); got != "claimed" {
		t.Errorf("triage ticket status = %q, want claimed; step 8 must not retire the "+
			"ticket — the coordinator does that at the gate, with the successor", got)
	}
	tags := mgField(t, triage, ".tags|join(\",\")")
	if !strings.Contains(tags, "declares-remainder") {
		t.Errorf("tags = %q, want declares-remainder retained; the remainder is still "+
			"owed and retracting the declaration would hide that", tags)
	}
	if strings.Contains(tags, "successor:") {
		t.Errorf("tags = %q: a successor was named before one could exist", tags)
	}
}

// TestRefusedDoneKeepsTheResultItWasGiven pins the guarantee mg-9259 created,
// which is the one this file used to assert the ABSENCE of.
//
// The old assertion here was that a refused `mg done` writes no sidecar. That was
// never the property being protected — it was the MEASUREMENT OF THE DEFECT that
// motivated mg-1912's relocation: proof that the sidecar route was unavailable at
// triage time, so the packet had to go somewhere else. mg-9259 removed the defect,
// and a measurement of a defect that no longer exists is not a control.
//
// Its stated rationale is falsified in both premises, which is why this is pogo's
// expectation to correct and not mg's behaviour to report. The old comment justified
// a store-wide glob on the grounds that a sidecar in claimed/ is "a packet nobody
// can find ... exactly the stray class `mg sidecars` exists to report". Measured
// against the shipped mg: the refusal PRINTS the sidecar's absolute path and says
// it survived, and `mg sidecars` does not call it stray — in claimed/, or in
// available/ after an unclaim. It is a companion to its item, not a stray.
//
// The direction settles it. pogo cannot hold as an invariant that its own triage
// packets are destroyed on refusal; surviving is the property mg-9259 was filed to
// create, and the old failure message conceded as much ("the packet was assumed
// lost on refusal and it is not").
//
// So this asserts THE WORK SURVIVES, which is strictly stronger than inverting the
// old check. A bare `len(hits) == 1` would pass on an empty file, on a paraphrase
// of the caller's payload, on a sidecar parked away from its item, and on one the
// retry then drops — every way "preserved" can be false while a sidecar exists.
func TestRefusedDoneKeepsTheResultItWasGiven(t *testing.T) {
	root := mgStore(t)

	triage := fileItem(t, "triage: probe (o/r#4)",
		"workflow: gh-issue\nstage: triage\ngh: o/r#4\n\nTriage this GitHub issue.")
	if tags := mgField(t, triage, ".tags|join(\",\")"); !strings.Contains(tags, "declares-remainder") {
		t.Fatalf("triage ticket tags = %q, want declares-remainder; without the carrier "+
			"there is no refusal to preserve a result across", tags)
	}
	if out, code := sh(t, "mg claim "+triage); code != 0 {
		t.Fatalf("mg claim: exit %d\n%s", code, out)
	}

	// A payload nothing else in the store could produce, so what comes back out is
	// demonstrably the caller's bytes rather than something mg synthesized.
	const payload = `{"workflow":"gh-issue","stage":"triage","recommendation":"decline","marker":"p6a0b-only"}`
	var want map[string]any
	if err := json.Unmarshal([]byte(payload), &want); err != nil {
		t.Fatalf("fixture payload is not JSON: %v", err)
	}

	out, code := sh(t, "mg done "+triage+" --result='"+payload+"'")
	if code == 0 {
		t.Fatalf("mg done --result succeeded on a declared item with no successor; the "+
			"refusal this control measures the COST of is gone:\n%s", out)
	}

	// Beside the item, in the directory the item is actually in. "Preserved" and
	// "stranded somewhere a reader of this item will never look" are different
	// outcomes and a store-wide glob is what tells them apart.
	hits := resultSidecars(t, root, triage)
	if len(hits) != 1 {
		t.Fatalf("want exactly one result sidecar preserved across the refusal, got %v", hits)
	}
	if wantPath := filepath.Join(root, "work", "claimed", triage+".result.json"); hits[0] != wantPath {
		t.Errorf("preserved sidecar at %s, want %s — beside the item, which is still claimed", hits[0], wantPath)
	}

	// The caller's packet, not a paraphrase of it. Compared decoded rather than
	// byte-wise so a re-serialization is allowed and a rewrite is not.
	data, err := os.ReadFile(hits[0])
	if err != nil {
		t.Fatalf("read preserved sidecar: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("preserved sidecar is not JSON: %v\n%s", err, data)
	}
	for key, wantVal := range want {
		if got[key] != wantVal {
			t.Errorf("preserved sidecar[%q] = %v, want %v", key, got[key], wantVal)
		}
	}

	// Preserved but unannounced is the hazard the old comment feared, and it would
	// be a fair one: an operator who watches a command exit non-zero assumes the
	// payload went with it. The refusal has to say where the packet is. Asserted as
	// "the message contains the path" rather than on wording, so a reword passes and
	// dropping the path — the regression that matters — does not.
	if !strings.Contains(out, hits[0]) {
		t.Errorf("the refusal does not name the preserved packet's location %s, so an "+
			"operator has no way to know it survived:\n%s", hits[0], out)
	}

	// And mg agrees it is not stray, which is the other half of "findable".
	strays, code := sh(t, "mg sidecars")
	if code != 0 {
		t.Fatalf("mg sidecars: exit %d\n%s", code, strays)
	}
	if strings.Contains(strays, triage) {
		t.Errorf("`mg sidecars` reports the preserved packet as stray:\n%s", strays)
	}

	// The proof of "preserved": the retry carries it through, WITHOUT the caller
	// re-supplying --result. This is what makes the refusal cost a retry rather
	// than the work — a sidecar that the successful completion then dropped would
	// satisfy every check above and still lose the packet.
	build := fileItem(t, "build: probe (o/r#4)", "workflow: gh-issue\nstage: build\ngh: o/r#4")
	if out, code := sh(t, "mg done "+triage+" --successor="+build); code != 0 {
		t.Fatalf("the retry failed: exit %d\n%s", code, out)
	}
	hits = resultSidecars(t, root, triage)
	if len(hits) != 1 {
		t.Fatalf("want exactly one result sidecar after the retry, got %v", hits)
	}
	if wantPath := filepath.Join(root, "work", "done", triage+".result.json"); hits[0] != wantPath {
		t.Errorf("sidecar at %s after the retry, want %s — it travels with the item", hits[0], wantPath)
	}
	data, err = os.ReadFile(hits[0])
	if err != nil {
		t.Fatalf("read carried sidecar: %v", err)
	}
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("carried sidecar is not JSON: %v\n%s", err, data)
	}
	for key, wantVal := range want {
		if got[key] != wantVal {
			t.Errorf("after the retry sidecar[%q] = %v, want %v; the packet the refusal "+
				"preserved did not survive the completion that followed it", key, got[key], wantVal)
		}
	}
}

// TestTriagePacketReachesTheSidecarAtTheGate is the downstream half: once the
// build ticket exists, mayor.md transition 3 promotes the worker's block into
// the result sidecar verbatim. This is the path that always worked, kept as a
// control so the relocation does not quietly cost the control-plane record.
func TestTriagePacketReachesTheSidecarAtTheGate(t *testing.T) {
	root := mgStore(t)

	triage := fileItem(t, "triage: probe (o/r#2)",
		"workflow: gh-issue\nstage: triage\ngh: o/r#2\n\nTriage this GitHub issue.")
	if out, code := sh(t, "mg claim "+triage); code != 0 {
		t.Fatalf("mg claim: exit %d\n%s", code, out)
	}

	block := shippedBashBlock(t, "prompts/templates/polecat-triage.md", "--append-body-file")
	scratch := t.TempDir()
	if out, code := sh(t, strings.NewReplacer("{{.Id}}", triage, "/tmp/", scratch+"/").Replace(block)); code != 0 {
		t.Fatalf("step 8: exit %d\n%s", code, out)
	}
	wantPacket := mgField(t, triage, ".body")

	// The gate opens; the build ticket is filed; only now is there an id to name.
	build := fileItem(t, "build: probe (o/r#2)", "workflow: gh-issue\nstage: build\ngh: o/r#2")

	mayor := shippedBashBlock(t, "prompts/mayor.md", "PACKET=$(mg show")
	script := strings.NewReplacer("<triage ticket id>", triage, "<build ticket id>", build).Replace(mayor)
	if out, code := sh(t, script); code != 0 {
		t.Fatalf("mayor.md transition 3 failed: exit %d\n%s\n--- script ---\n%s", code, out, script)
	}

	hits := resultSidecars(t, root, triage)
	if len(hits) != 1 {
		t.Fatalf("want exactly one result sidecar for %s, got %v", triage, hits)
	}
	// Beside the item, not stranded in the directory it used to live in — a
	// sidecar left behind by the move is the stray class `mg sidecars` reports.
	if want := filepath.Join(root, "work", "done", triage+".result.json"); hits[0] != want {
		t.Errorf("sidecar at %s, want %s", hits[0], want)
	}
	data, err := os.ReadFile(hits[0])
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("sidecar is not JSON: %v\n%s", err, data)
	}
	// The sidecar holds the worker's keys, not a coordinator's paraphrase.
	for _, key := range []string{"workflow", "stage", "issue", "recommendation", "remainder", "proposed_public_reply"} {
		if _, ok := got[key]; !ok {
			t.Errorf("sidecar is missing the %q key the worker wrote", key)
		}
	}
	// And the body copy is still there. Two records is the intent: the body one
	// is what survived the gate, the sidecar is the control-plane promotion.
	if now := mgField(t, triage, ".body"); now != wantPacket {
		t.Errorf("retirement altered the triage ticket body; the packet block must survive it")
	}
	if tags := mgField(t, triage, ".tags|join(\",\")"); !strings.Contains(tags, "successor:"+build) {
		t.Errorf("tags = %q, want successor:%s recorded by the retirement", tags, build)
	}
}

// TestFabricatedSuccessorIsRefusedOnlyWhenTheIDIsUnknown measures the guard the
// anti-fabrication rule in polecat-triage.md step 8 has to carry on its own.
//
// mg catches an id that names nothing (exit 3). It cannot catch a real id that
// names the WRONG item — which is the mistake actually made, once, by an agent
// typing an id it had not read. That one succeeds silently and gates a live
// pending item on a ticket that can never complete. So the rule is enforced
// structurally against typos and by instruction against carelessness, and this
// test records which is which rather than implying mg covers both.
func TestFabricatedSuccessorIsRefusedOnlyWhenTheIDIsUnknown(t *testing.T) {
	mgStore(t)

	triage := fileItem(t, "triage: probe (o/r#3)",
		"workflow: gh-issue\nstage: triage\ngh: o/r#3\n\nTriage this GitHub issue.")
	if out, code := sh(t, "mg claim "+triage); code != 0 {
		t.Fatalf("mg claim: exit %d\n%s", code, out)
	}

	// Unknown id: refused.
	out, code := sh(t, "mg done "+triage+" --successor=mg-ffffffff")
	if code == 0 {
		t.Fatalf("mg done accepted a successor naming no work item:\n%s", out)
	}
	if got := mgField(t, triage, ".status"); got != "claimed" {
		t.Fatalf("status = %q after a refused successor, want claimed", got)
	}

	// Real but unrelated id: accepted, with no signal of any kind. This is the
	// hazard the template must talk the actor out of, because nothing else will.
	unrelated := fileItem(t, "unrelated work", "nothing to do with o/r#3")
	if out, code := sh(t, "mg done "+triage+" --successor="+unrelated); code != 0 {
		t.Fatalf("expected a real-but-unrelated successor to be accepted (that is the "+
			"hazard being recorded), got exit %d:\n%s", code, out)
	}
}
