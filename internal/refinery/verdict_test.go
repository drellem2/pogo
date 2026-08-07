package refinery

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateVerdict(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"object", `{"verdict":"pass"}`, ""},
		{"object with nesting", `{"verdict":"partial","unverified":["throughput"],"n":3}`, ""},
		// The shape rules. A sidecar reader asks for named fields; a bare
		// scalar or array is storable but not answerable.
		{"bare string", `"pass"`, "must be a JSON object"},
		{"array", `[{"verdict":"pass"}]`, "must be a JSON object"},
		{"null", `null`, "got null"},
		{"not json", `verdict=pass`, "must be a JSON object"},
		// `{}` is precisely the failure this field exists to end, and a shell
		// that expanded a variable to nothing produces it far more often than
		// an author means it.
		{"empty object", `{}`, "records nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateVerdict(json.RawMessage(tc.raw))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("ValidateVerdict(%s) = %v, want nil", tc.raw, err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("ValidateVerdict(%s) = nil, want %q", tc.raw, tc.wantErr)
			case tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("ValidateVerdict(%s) = %v, want it to mention %q", tc.raw, err, tc.wantErr)
			}
		})
	}
}

// The rejection has to happen at SUBMIT, because submit is the last moment the
// author is still running and can be told. A verdict refused after the merge
// would be a verdict destroyed by the machinery meant to record it — mg-dfea
// one layer up.
func TestSubmitRejectsAMalformedVerdictWhileTheAuthorCanStillHearIt(t *testing.T) {
	r := &Refinery{byID: map[string]*MergeRequest{}}
	_, err := r.Submit(MergeRequest{
		RepoPath: t.TempDir(),
		Branch:   "polecat-x",
		Verdict:  json.RawMessage(`{`),
	})
	if err == nil {
		t.Fatal("Submit accepted an unparseable verdict")
	}
	if !strings.Contains(err.Error(), "verdict") {
		t.Errorf("the error must name the offending argument, got %v", err)
	}
	if len(r.queue) != 0 {
		t.Errorf("a rejected submit must not queue the MR, queue=%d", len(r.queue))
	}
}
