package diagnostics

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hashicorp/go-hclog"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

const version = "0.0.1"

// DiagnosticSeverity indicates the severity of a diagnostic.
type DiagnosticSeverity string

const (
	SeverityError   DiagnosticSeverity = "error"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityInfo    DiagnosticSeverity = "info"
)

// Diagnostic represents a single lint/diagnostic finding.
type Diagnostic struct {
	File     string             `json:"file"`
	Line     int                `json:"line"`
	Column   int                `json:"column"`
	Severity DiagnosticSeverity `json:"severity"`
	Source   string             `json:"source"`
	Message  string             `json:"message"`
}

// ProjectDiagnostics holds all diagnostics for a single project.
type ProjectDiagnostics struct {
	Root        string       `json:"root"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Error       string       `json:"error,omitempty"`
}

// DiagnosticsRequest is the input to an Execute call.
type DiagnosticsRequest struct {
	// Values: "run", "get", "summary"
	Type        string `json:"type"`
	ProjectRoot string `json:"projectRoot"`
}

// DiagnosticsResponse is the output of an Execute call.
type DiagnosticsResponse struct {
	Projects []ProjectDiagnostics `json:"projects"`
	Total    int                  `json:"total"`
	Error    string               `json:"error,omitempty"`
}

// SummaryResponse provides aggregate counts.
type SummaryResponse struct {
	Projects    int            `json:"projects"`
	TotalErrors int            `json:"totalErrors"`
	TotalWarns  int            `json:"totalWarnings"`
	TotalInfos  int            `json:"totalInfos"`
	BySeverity  map[string]int `json:"bySeverity"`
	BySource    map[string]int `json:"bySource"`
	Error       string         `json:"error,omitempty"`
}

type ErrorResponse struct {
	ErrorCode int    `json:"errorCode"`
	Error     string `json:"error"`
}

// DiagnosticsPlugin implements the IPogoPlugin interface for diagnostics.
type DiagnosticsPlugin struct {
	mu       sync.RWMutex
	logger   hclog.Logger
	projects map[string]*ProjectDiagnostics
}

var Service = createDiagnosticsPlugin()

// New returns a factory function for the diagnostics builtin plugin.
func New() func() (pogoPlugin.IPogoPlugin, error) {
	return func() (pogoPlugin.IPogoPlugin, error) {
		return Service, nil
	}
}

func createDiagnosticsPlugin() *DiagnosticsPlugin {
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Info,
		Output:     os.Stderr,
		JSONFormat: true,
	})

	return &DiagnosticsPlugin{
		logger:   logger,
		projects: make(map[string]*ProjectDiagnostics),
	}
}

func (d *DiagnosticsPlugin) Info() *pogoPlugin.PluginInfoRes {
	return &pogoPlugin.PluginInfoRes{Version: version}
}

func (d *DiagnosticsPlugin) Execute(req string) string {
	var diagReq DiagnosticsRequest
	if err := json.Unmarshal([]byte(req), &diagReq); err != nil {
		return d.errorResponse(400, "Invalid request.")
	}

	switch diagReq.Type {
	case "run":
		return d.runDiagnostics(diagReq.ProjectRoot)
	case "get":
		return d.getDiagnostics(diagReq.ProjectRoot)
	case "summary":
		return d.getSummary()
	default:
		return d.errorResponse(404, "Unknown request type.")
	}
}

func (d *DiagnosticsPlugin) ProcessProject(req *pogoPlugin.IProcessProjectReq) error {
	path := (*req).Path()
	d.logger.Info("Processing project for diagnostics", "path", path)

	d.mu.Lock()
	if _, ok := d.projects[path]; !ok {
		d.projects[path] = &ProjectDiagnostics{
			Root:        path,
			Diagnostics: []Diagnostic{},
		}
	}
	d.mu.Unlock()

	go d.collectDiagnostics(path)
	return nil
}

// collectDiagnostics runs available linters on a project and stores results.
func (d *DiagnosticsPlugin) collectDiagnostics(projectRoot string) {
	var diagnostics []Diagnostic

	// Detect project type and run appropriate linters.
	if isGoProject(projectRoot) {
		diags := d.runGoVet(projectRoot)
		diagnostics = append(diagnostics, diags...)
	}

	d.mu.Lock()
	d.projects[projectRoot] = &ProjectDiagnostics{
		Root:        projectRoot,
		Diagnostics: diagnostics,
	}
	d.mu.Unlock()

	d.logger.Info("Collected diagnostics", "project", projectRoot, "count", len(diagnostics))
}

func (d *DiagnosticsPlugin) runDiagnostics(projectRoot string) string {
	if projectRoot == "" {
		// Run for all known projects
		d.mu.RLock()
		roots := make([]string, 0, len(d.projects))
		for root := range d.projects {
			roots = append(roots, root)
		}
		d.mu.RUnlock()

		for _, root := range roots {
			d.collectDiagnostics(root)
		}
	} else {
		d.collectDiagnostics(projectRoot)
	}

	return d.getDiagnostics(projectRoot)
}

func (d *DiagnosticsPlugin) getDiagnostics(projectRoot string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	resp := DiagnosticsResponse{
		Projects: []ProjectDiagnostics{},
	}

	if projectRoot != "" {
		if proj, ok := d.projects[projectRoot]; ok {
			resp.Projects = append(resp.Projects, *proj)
			resp.Total = len(proj.Diagnostics)
		}
	} else {
		total := 0
		for _, proj := range d.projects {
			resp.Projects = append(resp.Projects, *proj)
			total += len(proj.Diagnostics)
		}
		resp.Total = total
	}

	bytes, err := json.Marshal(&resp)
	if err != nil {
		return d.errorResponse(500, "Error encoding response.")
	}
	return string(bytes)
}

func (d *DiagnosticsPlugin) getSummary() string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	resp := SummaryResponse{
		BySeverity: map[string]int{},
		BySource:   map[string]int{},
	}

	for range d.projects {
		resp.Projects++
	}
	for _, proj := range d.projects {
		for _, diag := range proj.Diagnostics {
			switch diag.Severity {
			case SeverityError:
				resp.TotalErrors++
			case SeverityWarning:
				resp.TotalWarns++
			case SeverityInfo:
				resp.TotalInfos++
			}
			resp.BySeverity[string(diag.Severity)]++
			resp.BySource[diag.Source]++
		}
	}

	bytes, err := json.Marshal(&resp)
	if err != nil {
		return d.errorResponse(500, "Error encoding response.")
	}
	return string(bytes)
}

func (d *DiagnosticsPlugin) errorResponse(code int, message string) string {
	resp := ErrorResponse{ErrorCode: code, Error: message}
	bytes, err := json.Marshal(&resp)
	if err != nil {
		d.logger.Error("Error writing error response")
		return `{"errorCode":500,"error":"internal error"}`
	}
	return string(bytes)
}

// GetAllDiagnostics returns all stored diagnostics (for direct access from pogod).
func (d *DiagnosticsPlugin) GetAllDiagnostics() []ProjectDiagnostics {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]ProjectDiagnostics, 0, len(d.projects))
	for _, proj := range d.projects {
		result = append(result, *proj)
	}
	return result
}

// isGoProject checks if a directory contains a go.mod file.
func isGoProject(root string) bool {
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil
}

// runGoVet runs `go vet` on a Go project and parses the output into diagnostics.
func (d *DiagnosticsPlugin) runGoVet(projectRoot string) []Diagnostic {
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = projectRoot

	output, err := cmd.CombinedOutput()
	if err == nil {
		// go vet returned 0 — no issues
		return nil
	}

	return parseGoVetOutput(projectRoot, string(output))
}

// parseGoVetOutput parses go vet stderr output into Diagnostic structs.
// Format: file.go:line:col: message
func parseGoVetOutput(projectRoot string, output string) []Diagnostic {
	var diagnostics []Diagnostic
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Expected format: path/file.go:line:col: message
		// or:              path/file.go:line: message
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 3 {
			continue
		}

		file := parts[0]
		lineNum := 0
		col := 0
		msg := ""

		if _, err := json.Number(parts[1]).Int64(); err == nil {
			n, _ := json.Number(parts[1]).Int64()
			lineNum = int(n)
		} else {
			continue
		}

		if len(parts) == 4 {
			if n, err := json.Number(parts[2]).Int64(); err == nil {
				col = int(n)
				msg = strings.TrimSpace(parts[3])
			} else {
				msg = strings.TrimSpace(parts[2] + ":" + parts[3])
			}
		} else {
			msg = strings.TrimSpace(parts[2])
		}

		diagnostics = append(diagnostics, Diagnostic{
			File:     file,
			Line:     lineNum,
			Column:   col,
			Severity: SeverityWarning,
			Source:   "go vet",
			Message:  msg,
		})
	}
	return diagnostics
}
