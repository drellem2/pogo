// Package mgcontract names the behaviours of the external `mg` binary that
// pogo's test suite depends on, and gives each one a probe.
//
// # The defect this exists for
//
// pogo's tests assert the behaviour of whatever `mg` happens to be installed,
// across a repo boundary, with no contract and no pinning. On 2026-08-07 two
// CORRECT macguffin changes, ninety minutes apart, each turned pogo's main red:
//
//	mg-d639  `mg mail send` began REFUSING an unknown recipient instead of
//	         silently creating a phantom mailbox. internal/strandedmail's
//	         fixtures seeded mail at invented names, so they broke — along with
//	         five further sites the whole-gate run then found.
//	mg-9259  a refused `mg done` began PRESERVING the result sidecar.
//	         internal/agent asserted the old behaviour and broke.
//
// Neither change was at fault; both were improvements pogo had asked for. The
// cost was structural and it was large: `build.sh` runs `go test ./...` and the
// refinery gate runs `build.sh`, so ONE such test takes down EVERY pogo merge.
// The second outage killed five branches, each of which needed an individual
// exoneration, and three agents attributed it to the wrong mg change because
// the failure shape was identical to the first.
//
// # What a clause buys
//
// A clause turns "an unrelated assertion in someone else's package broke" into
// "a NAMED contract with mg no longer holds". The failure arrives once, in this
// package, saying which behaviour changed, which mg work item created the one
// pogo expects, and which pogo tests rest on it. That is the whole point: the
// gate still goes red — pogo genuinely has a stale expectation and somebody has
// to decide which side is wrong — but the occurrence costs a glance instead of a
// full-suite hunt and three misattributions.
//
// # How it is used
//
// A test that depends on mg behaving a particular way says so:
//
//	func TestSomethingAboutRefusals(t *testing.T) {
//	    mgcontract.Require(t, mgcontract.DoneRefusalPreservesTheResultSidecar)
//	    ...
//	}
//
// Require probes the installed binary once per process per clause. If the clause
// holds, the test proceeds unchanged. If it does not, the test SKIPS — its
// premise is gone, so whatever it goes on to assert is not a finding about
// pogo — and TestTheDeclaredMgContractStillHolds in this package fails, by name.
// Nothing is hidden by the skip: the contract test is red, so the gate is red,
// so nothing merges until a human or an agent rules on it.
//
// # The rule that comes with it
//
// DO NOT resolve a broken clause by editing the probe to agree with what mg now
// does. A clause edited to match the dependency has stopped testing anything,
// and the pogo tests behind it are then asserting a behaviour nobody checked.
// Decide which side is wrong first — mayor's framing on mg-6a0b — then either
// fix pogo and re-declare the clause with its new `Since`, or file against mg.
//
// # What this does not do
//
// It does not make the dependent tests hermetic. Some of them must not be: the
// value of internal/strandedmail's live control is precisely that a fake cannot
// notice mg renaming an NDJSON field. Those tests stay live and declare their
// clauses. Tests that use `mg` only as a FIXTURE BUILDER — where the binary is
// incidental setup rather than the thing under test — belong on a stub instead;
// internal/ghintake is the worked example (MGSource{Bin: stub} for the cases,
// one live control for the wire format). See docs/design/mg-contract.md.
package mgcontract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Clause names. Referenced as constants rather than string literals so that
// retiring a clause is a COMPILE error at every test that rested on it, instead
// of a Require call that silently matches nothing.
const (
	NewPrintsTheCreatedID                   = "new/prints-the-created-id"
	NewStageTriageDeclaresRemainder         = "new/stage-triage-tags-declares-remainder"
	ShowJSONCarriesStatusTagsBody           = "show/json-carries-status-tags-body"
	ListClaimedJSONIsEmptyOnAnEmptyStore    = "list/claimed-json-is-empty-on-an-empty-store"
	ListClaimedRenderedNoticesAnEmptyStore  = "list/claimed-rendered-notices-an-empty-store"
	UnclaimReturnsTheItemToAvailable        = "unclaim/returns-the-item-to-available"
	DoneRefusesAnUnclaimedItem              = "done/refuses-an-unclaimed-item"
	DoneRefusesADeclaredItemWithNoSuccessor = "done/refuses-a-declared-item-with-no-successor"
	DoneRefusalPreservesTheResultSidecar    = "done/refusal-preserves-the-result-sidecar"
	DoneRefusesAnUnknownSuccessor           = "done/refuses-an-unknown-successor"
	MailSendRefusesAnUnknownRecipient       = "mail/send-refuses-an-unknown-recipient"
	MailSendCreateRegistersANewMailbox      = "mail/send-create-registers-a-new-mailbox"
	MailListJSONReportsUnreadCounts         = "mail/list-json-reports-unread-counts"
	NewRecordsTheCreatorInFrontmatter       = "new/records-the-creator-in-frontmatter"
	DoneResultSidecarRecordsTheBranch       = "done/result-sidecar-records-the-branch"
	EventsJSONLRecordsLandings              = "events/jsonl-records-work-done-and-archive"
	MailIsAMaildirUnderTheStore             = "mail/is-a-maildir-under-the-store"
)

// A Clause is one behaviour of the `mg` CLI that pogo's tests rest on.
//
// Since names the macguffin work item that created the behaviour pogo expects,
// where it is known. It is not decoration: when a clause breaks, the first
// question is whether mg regressed or deliberately moved, and the fastest route
// to that answer is `mg show <Since>` on the change that put the behaviour there.
type Clause struct {
	Name string

	// Since is the mg work item that established this behaviour, or "" when the
	// behaviour predates any ticket pogo can name.
	Since string

	// Why says what breaks in pogo if the behaviour changes, in one sentence.
	Why string

	// Dependents names the pogo tests and packages that rest on this clause, so
	// a failure here says immediately what else to expect and what to re-read.
	Dependents []string

	// probe exercises the behaviour against a private store. It returns nil when
	// the clause holds and an error describing the observed behaviour otherwise.
	probe func(s *store) error
}

// clauses is the declared contract. Adding a case to a live mg-driven test means
// adding the clause it rests on here; that is the trade this package asks for.
var clauses = []Clause{
	{
		Name: NewPrintsTheCreatedID,
		Why:  "every mg-driven fixture builder in the suite scrapes the new item's id out of `mg new` output; without it no fixture can be built at all",
		Dependents: []string{
			"internal/agent (mgNewClaimed, fileItem)",
			"cmd/pogod (mgClaimedItem)",
			"cmd/pogo/staleclaims_test.go (mgIDFrom)",
		},
		probe: func(s *store) error {
			out, code, err := s.run("new", "--type=task", "--no-repo", "--title=contract probe")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("`mg new` exited %d:\n%s", code, out)
			}
			if id := mgID.FindString(out); id == "" {
				return fmt.Errorf("`mg new` printed no mg-<hex> id:\n%s", out)
			}
			return nil
		},
	},
	{
		Name:  NewStageTriageDeclaresRemainder,
		Since: "mg-1912",
		Why:   "the gh-issue triage fixtures need an item that DECLARES a remainder; without the carrier they exercise an ordinary task and pass while proving nothing",
		Dependents: []string{
			"internal/agent/triagepacket_live_test.go (all four controls)",
		},
		probe: func(s *store) error {
			id, err := s.newItem("triage: contract probe",
				"workflow: gh-issue\nstage: triage\ngh: o/r#0\n\nTriage this.")
			if err != nil {
				return err
			}
			tags, err := s.field(id, ".tags|join(\",\")")
			if err != nil {
				return err
			}
			if !strings.Contains(tags, "declares-remainder") {
				return fmt.Errorf("a body leading `stage: triage` produced tags %q, want declares-remainder among them", tags)
			}
			return nil
		},
	},
	{
		Name: ShowJSONCarriesStatusTagsBody,
		Why:  "the live controls read an item's state back through `mg show --json`; a renamed field reads as an empty string and every assertion on it goes quietly wrong",
		Dependents: []string{
			"internal/agent/triagepacket_live_test.go (mgField)",
		},
		probe: func(s *store) error {
			id, err := s.newItem("contract probe", "a body")
			if err != nil {
				return err
			}
			out, code, err := s.run("show", id, "--json")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("`mg show --json` exited %d:\n%s", code, out)
			}
			var item map[string]any
			if err := json.Unmarshal([]byte(out), &item); err != nil {
				return fmt.Errorf("`mg show --json` is not a JSON object: %v\n%s", err, out)
			}
			for _, key := range []string{"status", "tags", "body"} {
				if _, ok := item[key]; !ok {
					return fmt.Errorf("`mg show --json` has no %q field; keys are %v", key, sortedKeys(item))
				}
			}
			return nil
		},
	},
	{
		Name: ListClaimedJSONIsEmptyOnAnEmptyStore,
		Why:  "`pogo doctor`'s stale-claim count parses this stream; when mg printed a human notice onto it the count read 1 on a clean store",
		Dependents: []string{
			"cmd/pogo/staleclaims_test.go (TestRealMG_EmptyStoreContract, countClaimedWorkItems)",
		},
		probe: func(s *store) error {
			out, code, err := s.run("list", "--status=claimed", "--json")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("`mg list --status=claimed --json` exited %d:\n%s", code, out)
			}
			if strings.TrimSpace(out) != "" {
				return fmt.Errorf("`mg list --status=claimed --json` on an empty store printed %q, want nothing", out)
			}
			return nil
		},
	},
	{
		Name: ListClaimedRenderedNoticesAnEmptyStore,
		Why:  "the stub fixture for the doctor check transcribes mg's empty-store notice; if mg stops printing one the stub is a fiction",
		Dependents: []string{
			"cmd/pogo/staleclaims_test.go (mgEmptyStoreNotice)",
		},
		probe: func(s *store) error {
			out, code, err := s.run("list", "--status=claimed")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("`mg list --status=claimed` exited %d:\n%s", code, out)
			}
			if strings.TrimSpace(out) == "" {
				return errors.New("`mg list --status=claimed` on an empty store printed nothing on the rendered stream, want a notice")
			}
			return nil
		},
	},
	{
		Name: UnclaimReturnsTheItemToAvailable,
		Why:  "the claim releaser's whole job is that a dead polecat's item is dispatchable again; the live controls check the store agrees, by looking in available/",
		Dependents: []string{
			"internal/agent/claimrelease_test.go",
			"cmd/pogod/deferreddeath_test.go",
		},
		probe: func(s *store) error {
			id, err := s.newItem("contract probe", "a body")
			if err != nil {
				return err
			}
			if out, code, err := s.run("claim", id); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg claim` exited %d:\n%s", code, out)
			}
			claims, err := filepath.Glob(filepath.Join(s.root, "work", "claimed", id+".md.*"))
			if err != nil || len(claims) != 1 {
				return fmt.Errorf("after `mg claim` the store holds %v at work/claimed/<id>.md.<pid>, want exactly one", claims)
			}
			if out, code, err := s.run("unclaim", id); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg unclaim` exited %d:\n%s", code, out)
			}
			if _, err := os.Stat(filepath.Join(s.root, "work", "available", id+".md")); err != nil {
				return fmt.Errorf("after `mg unclaim` the item is not at work/available/%s.md: %v", id, err)
			}
			return nil
		},
	},
	{
		Name:  DoneRefusesAnUnclaimedItem,
		Since: "mg-2b71",
		Why: "pogod's merge close CLAIMS an unclaimed item before closing it, and declines to close a gated one, " +
			"purely because this refusal exists; if `mg done` ever accepts an unclaimed item the claim step is " +
			"dead weight and the decline stops being the only way to leave a parked item alone",
		Dependents: []string{
			"internal/client (CloseMGWorkItemAtMerge)",
			"cmd/pogod/mergeclose_live_test.go",
		},
		probe: func(s *store) error {
			id, err := s.newItem("contract probe", "a body")
			if err != nil {
				return err
			}
			// Deliberately NOT claimed: available/ is where a hand-submitted
			// branch's item sits at merge time.
			out, code, err := s.run("done", id, `--result={"marker":"contract-probe"}`)
			if err != nil {
				return err
			}
			if code == 0 {
				return fmt.Errorf("`mg done` SUCCEEDED on an UNCLAIMED item; the merge close's claim step and its "+
					"gated decline both rest on this refusal:\n%s", out)
			}
			return nil
		},
	},
	{
		Name:  DoneRefusesADeclaredItemWithNoSuccessor,
		Since: "mg-1912",
		Why:   "this refusal is the entire reason the triage packet was relocated into the ticket body; if it stops firing the relocation needs re-arguing rather than silently outliving its cause",
		Dependents: []string{
			"internal/agent/triagepacket_live_test.go (TestTriagePacketIsWrittenBeforeAnySuccessorExists)",
			"prompts/templates/polecat-triage.md (step 8)",
		},
		probe: func(s *store) error {
			id, err := s.claimedTriage()
			if err != nil {
				return err
			}
			out, code, err := s.run("done", id, `--result={"marker":"contract-probe"}`)
			if err != nil {
				return err
			}
			if code == 0 {
				return fmt.Errorf("`mg done --result` SUCCEEDED on a declared item naming no successor:\n%s", out)
			}
			return nil
		},
	},
	{
		Name:  DoneRefusalPreservesTheResultSidecar,
		Since: "mg-9259",
		Why:   "a refused `mg done` must cost a retry, never the work; internal/agent asserted the OPPOSITE until mg-6a0b, and that is one of the two outages this package was filed for",
		Dependents: []string{
			"internal/agent/triagepacket_live_test.go (TestRefusedDoneKeepsTheResultItWasGiven)",
		},
		probe: func(s *store) error {
			id, err := s.claimedTriage()
			if err != nil {
				return err
			}
			const marker = "mgcontract-probe"
			out, code, err := s.run("done", id, `--result={"marker":"`+marker+`"}`)
			if err != nil {
				return err
			}
			if code == 0 {
				return fmt.Errorf("`mg done --result` succeeded where a refusal was expected, so there is no refusal to preserve anything across:\n%s", out)
			}
			// Beside the item, in the directory the item is actually in.
			// "Preserved" and "parked where no reader of this item will look"
			// are different outcomes, and only a store-wide look tells them apart.
			hits, gerr := filepath.Glob(filepath.Join(s.root, "work", "*", id+".result.json"))
			if gerr != nil {
				return gerr
			}
			want := filepath.Join(s.root, "work", "claimed", id+".result.json")
			if len(hits) != 1 || hits[0] != want {
				return fmt.Errorf("after the refusal the result sidecars are %v, want exactly [%s]", hits, want)
			}
			data, rerr := os.ReadFile(hits[0])
			if rerr != nil {
				return fmt.Errorf("read the preserved sidecar: %v", rerr)
			}
			var got map[string]any
			if jerr := json.Unmarshal(data, &got); jerr != nil {
				return fmt.Errorf("the preserved sidecar is not JSON: %v\n%s", jerr, data)
			}
			if got["marker"] != marker {
				return fmt.Errorf("the preserved sidecar holds %v, want the caller's own payload marker %q", got, marker)
			}
			// Preserved but unannounced is a fair hazard: an operator watching a
			// command exit non-zero assumes the payload went with it. Asserted as
			// "the refusal names the path" so a reword passes and dropping the
			// path — the regression that matters — does not.
			if !strings.Contains(out, hits[0]) {
				return fmt.Errorf("the refusal does not name the preserved packet's location %s:\n%s", hits[0], out)
			}
			return nil
		},
	},
	{
		Name: DoneRefusesAnUnknownSuccessor,
		Why:  "the anti-fabrication rule in polecat-triage.md leans on mg catching an id that names nothing; the rule only has to carry the real-but-wrong id case on its own",
		Dependents: []string{
			"internal/agent/triagepacket_live_test.go (TestFabricatedSuccessorIsRefusedOnlyWhenTheIDIsUnknown)",
		},
		probe: func(s *store) error {
			id, err := s.claimedTriage()
			if err != nil {
				return err
			}
			out, code, err := s.run("done", id, "--successor=mg-ffffffff")
			if err != nil {
				return err
			}
			if code == 0 {
				return fmt.Errorf("`mg done --successor` accepted an id naming no work item:\n%s", out)
			}
			return nil
		},
	},
	{
		Name:  MailSendRefusesAnUnknownRecipient,
		Since: "mg-d639",
		Why:   "pogo's production senders rely on a typo'd recipient being CAUGHT rather than silently given a phantom mailbox; this is the other of the two outages this package was filed for",
		Dependents: []string{
			"internal/strandedmail",
			"cmd/pogo/checkstrandedmail.go and the deploy sink's wrong-recipient diagnosis",
		},
		probe: func(s *store) error {
			out, code, err := s.run("mail", "send", "nobodyhere", "--from=contract-probe", "--subject=probe", "--body=probe")
			if err != nil {
				return err
			}
			if code == 0 {
				return fmt.Errorf("`mg mail send` accepted an unknown recipient without --create:\n%s", out)
			}
			if _, serr := os.Stat(filepath.Join(s.root, "mail", "nobodyhere")); serr == nil {
				return errors.New("`mg mail send` refused an unknown recipient but created the mailbox anyway")
			}
			return nil
		},
	},
	{
		Name:  MailSendCreateRegistersANewMailbox,
		Since: "mg-d639",
		Why:   "every mail fixture in the suite seeds a box that is unknown by construction; --create is the door mg-d639's refusal deliberately left open for exactly that",
		Dependents: []string{
			"internal/strandedmail (mgSend)",
			"internal/agent and cmd/pogod mail fixtures",
		},
		probe: func(s *store) error {
			out, code, err := s.run("mail", "send", "somebodynew", "--create", "--from=contract-probe", "--subject=probe", "--body=probe")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("`mg mail send --create` exited %d on a new recipient:\n%s", code, out)
			}
			if _, serr := os.Stat(filepath.Join(s.root, "mail", "somebodynew")); serr != nil {
				return fmt.Errorf("`mg mail send --create` reported success but wrote no mailbox: %v", serr)
			}
			return nil
		},
	},
	{
		Name: MailListJSONReportsUnreadCounts,
		Why:  "the stranded-mail sweep decodes these NDJSON field names; rename `unread` and the sweep reads 0 for every mailbox and goes permanently, cheerfully quiet — the sweep's own bug shape, inside the thing built to catch it",
		Dependents: []string{
			"internal/strandedmail (EnumerateMailboxes, Detect)",
		},
		probe: func(s *store) error {
			if _, _, err := s.run("mail", "send", "countme", "--create", "--from=contract-probe", "--subject=probe", "--body=probe"); err != nil {
				return err
			}
			out, code, err := s.run("mail", "list", "--json")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("`mg mail list --json` exited %d:\n%s", code, out)
			}
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var box struct {
					Mailbox string `json:"mailbox"`
					Unread  *int   `json:"unread"`
				}
				if jerr := json.Unmarshal([]byte(line), &box); jerr != nil {
					return fmt.Errorf("`mg mail list --json` emitted a line that is not JSON: %v\n%s", jerr, line)
				}
				if box.Mailbox != "countme" {
					continue
				}
				if box.Unread == nil {
					return fmt.Errorf("`mg mail list --json` carries no `unread` field: %s", line)
				}
				if *box.Unread != 1 {
					return fmt.Errorf("`mg mail list --json` reports unread=%d for a box holding one unread message: %s", *box.Unread, line)
				}
				return nil
			}
			return fmt.Errorf("`mg mail list --json` never mentioned the mailbox just written to:\n%s", out)
		},
	},
	// ---- the four clauses `pogo check-verdicts` rests on (mg-f5dd) ----
	//
	// Its predicate reads BOTH halves out of macguffin's own store — the landing
	// from events.jsonl, the delivery from the maildir — and the identities that
	// join them from frontmatter and the result sidecar. Every one of these
	// clauses fails QUIETLY if it goes: the detector finds nothing to judge,
	// reports zero dropped verdicts, and exits 0. That is exactly the shape the
	// detector exists to catch, reproduced inside the detector, which is why the
	// dependencies are declared here rather than left implicit.
	{
		Name: NewRecordsTheCreatorInFrontmatter,
		Why:  "check-verdicts identifies the FILER — the party owed the verdict — from `creator:`; without it every landed item is skipped and the scan is cheerfully empty",
		Dependents: []string{
			"internal/verdictwatch (loadItems, Scan)",
			"cmd/pogo/checkverdicts.go",
		},
		probe: func(s *store) error {
			id, err := s.newItem("contract probe", "a body")
			if err != nil {
				return err
			}
			hits, gerr := filepath.Glob(filepath.Join(s.root, "work", "*", id+".md*"))
			if gerr != nil || len(hits) == 0 {
				return fmt.Errorf("the item just filed is not on disk at work/*/%s.md*: %v %v", id, hits, gerr)
			}
			data, rerr := os.ReadFile(hits[0])
			if rerr != nil {
				return rerr
			}
			if !regexp.MustCompile(`(?m)^creator:\s*\S`).Match(data) {
				return fmt.Errorf("a filed item carries no `creator:` frontmatter line:\n%s", data)
			}
			return nil
		},
	},
	{
		Name: DoneResultSidecarRecordsTheBranch,
		Why:  "the branch in the result sidecar is the ONLY worker identity macguffin records; lose it and every landed item goes UNDECIDABLE, which check-verdicts reports as neither delivered nor dropped",
		Dependents: []string{
			"internal/verdictwatch (sidecarWorker, resolveWorker)",
			"internal/verdictwatch (the constructive probe's `land` fixture)",
		},
		probe: func(s *store) error {
			id, err := s.newItem("contract probe", "a body")
			if err != nil {
				return err
			}
			if out, code, err := s.run("claim", id); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg claim` exited %d:\n%s", code, out)
			}
			if out, code, err := s.run("done", id, `--result={"branch":"polecat-probe1"}`); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg done --result` exited %d:\n%s", code, out)
			}
			want := filepath.Join(s.root, "work", "done", id+".result.json")
			data, rerr := os.ReadFile(want)
			if rerr != nil {
				return fmt.Errorf("after `mg done --result` there is no sidecar at %s: %v", want, rerr)
			}
			var side struct {
				Branch string `json:"branch"`
			}
			if jerr := json.Unmarshal(data, &side); jerr != nil {
				return fmt.Errorf("the result sidecar is not JSON: %v\n%s", jerr, data)
			}
			if side.Branch != "polecat-probe1" {
				return fmt.Errorf("the result sidecar records branch %q, want the caller's own %q", side.Branch, "polecat-probe1")
			}
			return nil
		},
	},
	{
		Name: EventsJSONLRecordsLandings,
		Why:  "events.jsonl is the LANDING half of check-verdicts' predicate; rename the type or the item_id field and every item reads as never landed, so the detector reports a clean fleet while every verdict in it goes missing",
		Dependents: []string{
			"internal/verdictwatch (loadLandings)",
		},
		probe: func(s *store) error {
			id, err := s.newItem("contract probe", "a body")
			if err != nil {
				return err
			}
			if out, code, err := s.run("claim", id); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg claim` exited %d:\n%s", code, out)
			}
			if out, code, err := s.run("done", id, `--result={"branch":"polecat-probe1"}`); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg done --result` exited %d:\n%s", code, out)
			}
			data, rerr := os.ReadFile(filepath.Join(s.root, "events.jsonl"))
			if rerr != nil {
				return fmt.Errorf("the store has no events.jsonl after a completed item: %v", rerr)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var e struct {
					Type   string `json:"type"`
					ItemID string `json:"item_id"`
					TS     string `json:"ts"`
				}
				if json.Unmarshal([]byte(line), &e) != nil {
					continue
				}
				if e.Type != "work.done" || e.ItemID != id {
					continue
				}
				if e.TS == "" {
					return fmt.Errorf("the work.done event for %s carries no `ts`, so a landing cannot be ordered or bounded by --since: %s", id, line)
				}
				return nil
			}
			return fmt.Errorf("no {\"type\":\"work.done\",\"item_id\":%q} line in events.jsonl after completing it:\n%s", id, data)
		},
	},
	{
		Name: MailIsAMaildirUnderTheStore,
		Why:  "check-verdicts reads the maildir DIRECTLY because `mg mail list --all` excludes archived mail and a filed-away verdict must still count as delivered; a layout or header change makes every landed item read as dropped",
		Dependents: []string{
			"internal/verdictwatch (loadMailbox)",
		},
		probe: func(s *store) error {
			const box, sender, subject = "maildirprobe", "contract-probe", "probe subject"
			if out, code, err := s.run("mail", "send", box, "--create", "--from="+sender, "--subject="+subject, "--body=probe"); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg mail send --create` exited %d:\n%s", code, out)
			}
			// Delivered under <root>/mail/<box>/{new,cur,archive}, with the
			// From/Subject/Date headers the detector matches a worker on.
			var found string
			for _, sub := range []string{"new", "cur", "archive"} {
				entries, err := os.ReadDir(filepath.Join(s.root, "mail", box, sub))
				if err != nil {
					continue
				}
				for _, e := range entries {
					if !e.IsDir() {
						found = filepath.Join(s.root, "mail", box, sub, e.Name())
					}
				}
			}
			if found == "" {
				return fmt.Errorf("nothing under %s/{new,cur,archive} after a delivered message", filepath.Join(s.root, "mail", box))
			}
			data, rerr := os.ReadFile(found)
			if rerr != nil {
				return rerr
			}
			head, _, _ := strings.Cut(string(data), "\n\n")
			for _, want := range []string{"From: " + sender, "Subject: " + subject, "Date: "} {
				if !strings.Contains(head, want) {
					return fmt.Errorf("the delivered message's headers do not carry %q:\n%s", want, head)
				}
			}
			// And an ARCHIVED message stays readable in the same tree. This is
			// the half `mg mail list --all` cannot answer, and the reason the
			// detector reads the maildir instead of the CLI.
			id, ierr := s.messageID(box, sender, subject)
			if ierr != nil {
				return ierr
			}
			if out, code, err := s.run("mail", "archive", box+"/"+id); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("`mg mail archive` exited %d:\n%s", code, out)
			}
			entries, aerr := os.ReadDir(filepath.Join(s.root, "mail", box, "archive"))
			if aerr != nil || len(entries) == 0 {
				return fmt.Errorf("after `mg mail archive` there is nothing under %s: %v",
					filepath.Join(s.root, "mail", box, "archive"), aerr)
			}
			return nil
		},
	},
}

// Clauses returns the declared contract, in declaration order.
func Clauses() []Clause {
	out := make([]Clause, len(clauses))
	copy(out, clauses)
	return out
}

// Lookup returns the clause with the given name.
func Lookup(name string) (Clause, bool) {
	for _, c := range clauses {
		if c.Name == name {
			return c, true
		}
	}
	return Clause{}, false
}

// ErrMgNotInstalled is what Verify reports for every clause on a machine with no
// `mg` on PATH. It is not a contract failure: this suite runs on machines
// without the fleet's tooling, and always has.
var ErrMgNotInstalled = errors.New("mg is not on PATH")

// Result is one clause's verdict against the installed binary.
type Result struct {
	Clause Clause
	Err    error // nil when the clause holds
}

// Holds reports whether the clause was observed to hold.
func (r Result) Holds() bool { return r.Err == nil }

var (
	mu      sync.Mutex
	results = map[string]Result{}
)

// Require verifies that every named clause holds against the installed `mg`, and
// SKIPS the calling test if any does not.
//
// The skip is deliberate and it is not a way of hiding anything. A test whose
// premise about mg is gone cannot produce a finding about pogo — whatever it
// asserts next is arithmetic on a fiction, and the five branches killed on
// 2026-08-07 were each blamed by exactly that kind of assertion. The red belongs
// in ONE place, TestTheDeclaredMgContractStillHolds, which names the clause, the
// mg item that created it, and every pogo test resting on it. That test is in
// `go test ./...`, so the gate is still red and nothing merges until somebody
// rules on which side is wrong.
//
// A machine with no `mg` skips, as these tests always have.
func Require(t testing.TB, names ...string) {
	t.Helper()
	for _, name := range names {
		res := Verify(name)
		if res.Holds() {
			continue
		}
		if errors.Is(res.Err, ErrMgNotInstalled) {
			t.Skipf("mg is not on PATH; this test drives the real macguffin CLI")
		}
		t.Skipf("mg contract clause %q no longer holds, so this test's premise is gone:\n"+
			"  %v\n"+
			"The red for this belongs to TestTheDeclaredMgContractStillHolds in internal/mgcontract, "+
			"which names the change. Decide which side is wrong before touching either — do NOT edit "+
			"an assertion to agree with whatever mg now does.", name, res.Err)
	}
}

// Verify probes one clause and returns its verdict. The probe runs at most once
// per clause per process; every later call reads the cached verdict, so a suite
// that requires the same clause in twenty tests pays for it once.
func Verify(name string) Result {
	mu.Lock()
	defer mu.Unlock()
	if res, ok := results[name]; ok {
		return res
	}
	clause, ok := Lookup(name)
	if !ok {
		res := Result{Clause: Clause{Name: name}, Err: fmt.Errorf("no clause named %q is declared in internal/mgcontract", name)}
		results[name] = res
		return res
	}
	res := Result{Clause: clause, Err: probe(clause)}
	results[name] = res
	return res
}

// VerifyAll probes every declared clause and returns the verdicts in
// declaration order.
func VerifyAll() []Result {
	out := make([]Result, 0, len(clauses))
	for _, c := range clauses {
		out = append(out, Verify(c.Name))
	}
	return out
}

// Installed reports whether `mg` is on PATH.
func Installed() bool {
	_, err := exec.LookPath("mg")
	return err == nil
}

// probe stands up a private store for the clause and runs it there.
func probe(c Clause) error {
	if !Installed() {
		return ErrMgNotInstalled
	}
	dir, err := os.MkdirTemp("", "mgcontract-")
	if err != nil {
		return fmt.Errorf("create a private store: %w", err)
	}
	defer os.RemoveAll(dir)
	s := &store{root: filepath.Join(dir, "macguffin")}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("create a private store: %w", err)
	}
	if out, code, err := s.run("init"); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("`mg init` on a private store exited %d:\n%s", code, out)
	}
	return c.probe(s)
}

// store is a throwaway macguffin workspace.
//
// Every command is addressed with the --root FLAG rather than $MG_ROOT. The
// difference is not cosmetic: Require may be called from tests running in
// parallel, and os.Setenv is process-wide, so an env-var store would be a race
// between the probe and whatever the calling package has pinned. The flag
// carries the same authority — mg resolves --root above $MG_ROOT above
// $HOME/.macguffin — and carries it per-command.
type store struct{ root string }

func (s *store) run(args ...string) (string, int, error) {
	cmd := exec.Command("mg", append([]string{"--root", s.root}, args...)...)
	// A probe must not inherit the caller's store by any route. --root already
	// wins over MG_ROOT, and this makes that independent of mg's flag precedence.
	cmd.Env = append(os.Environ(), "MG_ROOT="+s.root)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	if err != nil {
		return string(out), -1, fmt.Errorf("run `mg %s`: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), 0, nil
}

var mgID = regexp.MustCompile(`\bmg-[0-9a-f]{4,}\b`)

// newItem files an item with --no-repo: these fixtures are about nothing, and
// resolving a repo from the cwd would record whichever ephemeral worktree the
// test binary happens to be running in.
func (s *store) newItem(title, body string) (string, error) {
	out, code, err := s.run("new", "--type=task", "--no-repo", "--title="+title, "--body="+body)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg new` exited %d:\n%s", code, out)
	}
	id := mgID.FindString(out)
	if id == "" {
		return "", fmt.Errorf("`mg new` printed no work item id:\n%s", out)
	}
	return id, nil
}

// claimedTriage files and claims an item carrying the `stage: triage` body lead,
// which is what makes it declare a remainder — the state the three `mg done`
// clauses are all about.
func (s *store) claimedTriage() (string, error) {
	id, err := s.newItem("triage: contract probe",
		"workflow: gh-issue\nstage: triage\ngh: o/r#0\n\nTriage this.")
	if err != nil {
		return "", err
	}
	tags, err := s.field(id, ".tags|join(\",\")")
	if err != nil {
		return "", err
	}
	if !strings.Contains(tags, "declares-remainder") {
		return "", fmt.Errorf("the fixture does not declare a remainder (tags %q), so it is not the shape this clause is about; see clause %q",
			tags, NewStageTriageDeclaresRemainder)
	}
	out, code, err := s.run("claim", id)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg claim` exited %d:\n%s", code, out)
	}
	return id, nil
}

// messageID recovers the MSG-ID of a delivered message from the documented
// `mg mail list --json` contract.
//
// Read from --json rather than scraped out of `mg mail send`'s output, which
// prints a PATH: the original verdictwatch suite scraped it, matched nothing,
// and silently asserted on a setup that had never happened.
func (s *store) messageID(box, from, subject string) (string, error) {
	out, code, err := s.run("mail", "list", box, "--all", "--json")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg mail list %s --all --json` exited %d:\n%s", box, code, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec struct {
			ID      string `json:"id"`
			From    string `json:"from"`
			Subject string `json:"subject"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.From == from && rec.Subject == subject {
			return rec.ID, nil
		}
	}
	return "", fmt.Errorf("`mg mail list %s --all --json` does not list the message just sent:\n%s", box, out)
}

// field reads one jq-ish path out of `mg show --json`. Implemented in Go rather
// than by shelling out to jq so the probes have one external dependency (mg)
// instead of two — a clause that skipped because jq was missing would be a
// second, unrelated way for the contract to go quiet.
func (s *store) field(id, path string) (string, error) {
	out, code, err := s.run("show", id, "--json")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg show --json` exited %d:\n%s", code, out)
	}
	var item map[string]any
	if jerr := json.Unmarshal([]byte(out), &item); jerr != nil {
		return "", fmt.Errorf("`mg show --json` is not a JSON object: %v\n%s", jerr, out)
	}
	switch path {
	case ".status":
		s, _ := item["status"].(string)
		return s, nil
	case ".body":
		s, _ := item["body"].(string)
		return s, nil
	case ".tags|join(\",\")":
		raw, _ := item["tags"].([]any)
		parts := make([]string, 0, len(raw))
		for _, t := range raw {
			parts = append(parts, fmt.Sprint(t))
		}
		return strings.Join(parts, ","), nil
	}
	return "", fmt.Errorf("mgcontract: unsupported field path %q", path)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
