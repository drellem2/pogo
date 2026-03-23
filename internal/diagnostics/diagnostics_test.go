package diagnostics

import (
	"encoding/json"
	"testing"

	"github.com/drellem2/pogo/pkg/plugin"
)

func TestInfo(t *testing.T) {
	d := createDiagnosticsPlugin()
	info := d.Info()
	if info.Version != version {
		t.Errorf("Expected version %s, got %s", version, info.Version)
	}
}

func TestExecuteInvalidRequest(t *testing.T) {
	d := createDiagnosticsPlugin()
	resp := d.Execute("not json")

	var errResp ErrorResponse
	if err := json.Unmarshal([]byte(resp), &errResp); err != nil {
		t.Fatalf("Could not parse error response: %v", err)
	}
	if errResp.ErrorCode != 400 {
		t.Errorf("Expected error code 400, got %d", errResp.ErrorCode)
	}
}

func TestExecuteUnknownType(t *testing.T) {
	d := createDiagnosticsPlugin()
	req := `{"type":"unknown"}`
	resp := d.Execute(req)

	var errResp ErrorResponse
	if err := json.Unmarshal([]byte(resp), &errResp); err != nil {
		t.Fatalf("Could not parse error response: %v", err)
	}
	if errResp.ErrorCode != 404 {
		t.Errorf("Expected error code 404, got %d", errResp.ErrorCode)
	}
}

func TestGetEmptyDiagnostics(t *testing.T) {
	d := createDiagnosticsPlugin()
	req := `{"type":"get"}`
	resp := d.Execute(req)

	var diagResp DiagnosticsResponse
	if err := json.Unmarshal([]byte(resp), &diagResp); err != nil {
		t.Fatalf("Could not parse response: %v", err)
	}
	if diagResp.Total != 0 {
		t.Errorf("Expected 0 total diagnostics, got %d", diagResp.Total)
	}
	if len(diagResp.Projects) != 0 {
		t.Errorf("Expected 0 projects, got %d", len(diagResp.Projects))
	}
}

func TestGetSummaryEmpty(t *testing.T) {
	d := createDiagnosticsPlugin()
	req := `{"type":"summary"}`
	resp := d.Execute(req)

	var summary SummaryResponse
	if err := json.Unmarshal([]byte(resp), &summary); err != nil {
		t.Fatalf("Could not parse response: %v", err)
	}
	if summary.Projects != 0 {
		t.Errorf("Expected 0 projects, got %d", summary.Projects)
	}
	if summary.TotalErrors != 0 {
		t.Errorf("Expected 0 errors, got %d", summary.TotalErrors)
	}
}

func TestProcessProject(t *testing.T) {
	d := createDiagnosticsPlugin()
	var req plugin.IProcessProjectReq = plugin.ProcessProjectReq{PathVar: "/tmp/nonexistent-project/"}
	err := d.ProcessProject(&req)
	if err != nil {
		t.Errorf("ProcessProject returned error: %v", err)
	}

	// Project should be registered
	d.mu.RLock()
	_, ok := d.projects["/tmp/nonexistent-project/"]
	d.mu.RUnlock()
	if !ok {
		t.Error("Expected project to be registered after ProcessProject")
	}
}

func TestParseGoVetOutput(t *testing.T) {
	output := `# example.com/pkg
pkg/foo.go:10:5: unreachable code
pkg/bar.go:25: missing return
# example.com/other
`
	diags := parseGoVetOutput("/project", output)
	if len(diags) != 2 {
		t.Fatalf("Expected 2 diagnostics, got %d", len(diags))
	}

	d := diags[0]
	if d.File != "pkg/foo.go" {
		t.Errorf("Expected file pkg/foo.go, got %s", d.File)
	}
	if d.Line != 10 {
		t.Errorf("Expected line 10, got %d", d.Line)
	}
	if d.Column != 5 {
		t.Errorf("Expected column 5, got %d", d.Column)
	}
	if d.Message != "unreachable code" {
		t.Errorf("Expected message 'unreachable code', got '%s'", d.Message)
	}
	if d.Severity != SeverityWarning {
		t.Errorf("Expected severity warning, got %s", d.Severity)
	}
	if d.Source != "go vet" {
		t.Errorf("Expected source 'go vet', got '%s'", d.Source)
	}

	d2 := diags[1]
	if d2.File != "pkg/bar.go" {
		t.Errorf("Expected file pkg/bar.go, got %s", d2.File)
	}
	if d2.Line != 25 {
		t.Errorf("Expected line 25, got %d", d2.Line)
	}
}

func TestNewFactory(t *testing.T) {
	factory := New()
	p, err := factory()
	if err != nil {
		t.Fatalf("Factory returned error: %v", err)
	}
	if p == nil {
		t.Fatal("Factory returned nil plugin")
	}
	info := p.Info()
	if info.Version != version {
		t.Errorf("Expected version %s, got %s", version, info.Version)
	}
}

func TestGetAllDiagnostics(t *testing.T) {
	d := createDiagnosticsPlugin()

	// Add some diagnostics manually
	d.projects["/project/a/"] = &ProjectDiagnostics{
		Root: "/project/a/",
		Diagnostics: []Diagnostic{
			{File: "main.go", Line: 1, Severity: SeverityError, Source: "go vet", Message: "test"},
		},
	}
	d.projects["/project/b/"] = &ProjectDiagnostics{
		Root:        "/project/b/",
		Diagnostics: []Diagnostic{},
	}

	all := d.GetAllDiagnostics()
	if len(all) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(all))
	}
}

func TestSummaryAggregation(t *testing.T) {
	d := createDiagnosticsPlugin()
	d.projects["/a/"] = &ProjectDiagnostics{
		Root: "/a/",
		Diagnostics: []Diagnostic{
			{Severity: SeverityError, Source: "go vet"},
			{Severity: SeverityError, Source: "go vet"},
			{Severity: SeverityWarning, Source: "go vet"},
			{Severity: SeverityInfo, Source: "go vet"},
		},
	}
	d.projects["/b/"] = &ProjectDiagnostics{
		Root: "/b/",
		Diagnostics: []Diagnostic{
			{Severity: SeverityWarning, Source: "go vet"},
		},
	}

	resp := d.Execute(`{"type":"summary"}`)
	var summary SummaryResponse
	if err := json.Unmarshal([]byte(resp), &summary); err != nil {
		t.Fatalf("Could not parse summary: %v", err)
	}
	if summary.Projects != 2 {
		t.Errorf("Expected 2 projects, got %d", summary.Projects)
	}
	if summary.TotalErrors != 2 {
		t.Errorf("Expected 2 errors, got %d", summary.TotalErrors)
	}
	if summary.TotalWarns != 2 {
		t.Errorf("Expected 2 warnings, got %d", summary.TotalWarns)
	}
	if summary.TotalInfos != 1 {
		t.Errorf("Expected 1 info, got %d", summary.TotalInfos)
	}
	if summary.BySeverity["error"] != 2 {
		t.Errorf("Expected bySeverity[error]=2, got %d", summary.BySeverity["error"])
	}
	if summary.BySource["go vet"] != 5 {
		t.Errorf("Expected bySource[go vet]=5, got %d", summary.BySource["go vet"])
	}
}
