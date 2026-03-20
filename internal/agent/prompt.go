package agent

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed prompts
var defaultPrompts embed.FS

// PromptDir returns the root directory for agent prompt files.
// Default: ~/.pogo/agents/
func PromptDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo", "agents")
}

// TemplateDir returns the directory for polecat prompt templates.
// Default: ~/.pogo/agents/templates/
func TemplateDir() string {
	return filepath.Join(PromptDir(), "templates")
}

// TemplateVars holds the variables available during polecat template expansion.
type TemplateVars struct {
	Task string // Work item title
	Body string // Work item body (markdown)
	Id   string // Work item ID
	Repo string // Target repository path
}

// ResolveCrewPrompt returns the path to a crew agent's prompt file.
// Returns an error if the file does not exist.
func ResolveCrewPrompt(name string) (string, error) {
	path := filepath.Join(CrewPromptDir(), name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("crew prompt not found: %s", path)
	} else if err != nil {
		return "", fmt.Errorf("stat crew prompt: %w", err)
	}
	return path, nil
}

// ResolveMayorPrompt returns the path to the mayor's prompt file.
// Returns an error if the file does not exist.
func ResolveMayorPrompt() (string, error) {
	path := filepath.Join(PromptDir(), "mayor.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("mayor prompt not found: %s", path)
	} else if err != nil {
		return "", fmt.Errorf("stat mayor prompt: %w", err)
	}
	return path, nil
}

// ResolveTemplate returns the path to a polecat template file.
// Returns an error if the file does not exist.
func ResolveTemplate(name string) (string, error) {
	path := filepath.Join(TemplateDir(), name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("template not found: %s", path)
	} else if err != nil {
		return "", fmt.Errorf("stat template: %w", err)
	}
	return path, nil
}

// ExpandTemplate reads a template file and expands {{.Variable}} placeholders
// with the provided vars. Uses Go text/template syntax.
func ExpandTemplate(templatePath string, vars TemplateVars) (string, error) {
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read template: %w", err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

// ExpandTemplateToFile expands a template and writes it to a temporary file.
// The caller is responsible for removing the temp file when done.
// Returns the path to the generated file.
func ExpandTemplateToFile(templatePath string, vars TemplateVars) (string, error) {
	expanded, err := ExpandTemplate(templatePath, vars)
	if err != nil {
		return "", err
	}

	// Write to a temp file in the pogo runtime directory
	tmpDir := filepath.Join(os.TempDir(), "pogo-prompts")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return "", fmt.Errorf("create prompt temp dir: %w", err)
	}

	f, err := os.CreateTemp(tmpDir, "polecat-*.md")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(expanded); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write expanded prompt: %w", err)
	}

	return f.Name(), nil
}

// PromptInfo describes a discovered prompt file.
type PromptInfo struct {
	Name     string `json:"name"`     // File stem (e.g., "arch", "polecat")
	Path     string `json:"path"`     // Full filesystem path
	Category string `json:"category"` // "crew", "templates", or "mayor"
}

// ListPrompts discovers all prompt files under ~/.pogo/agents/.
func ListPrompts() ([]PromptInfo, error) {
	root := PromptDir()
	var prompts []PromptInfo

	// Mayor prompt (top-level)
	mayorPath := filepath.Join(root, "mayor.md")
	if _, err := os.Stat(mayorPath); err == nil {
		prompts = append(prompts, PromptInfo{
			Name:     "mayor",
			Path:     mayorPath,
			Category: "mayor",
		})
	}

	// Crew prompts
	crewDir := CrewPromptDir()
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			prompts = append(prompts, PromptInfo{
				Name:     name,
				Path:     filepath.Join(crewDir, e.Name()),
				Category: "crew",
			})
		}
	}

	// Templates
	tmplDir := TemplateDir()
	if entries, err := os.ReadDir(tmplDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			prompts = append(prompts, PromptInfo{
				Name:     name,
				Path:     filepath.Join(tmplDir, e.Name()),
				Category: "templates",
			})
		}
	}

	return prompts, nil
}

// InitPromptDirs creates the ~/.pogo/agents/ directory structure.
func InitPromptDirs() error {
	dirs := []string{
		CrewPromptDir(),
		TemplateDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// InstallResult describes what happened during prompt installation.
type InstallResult struct {
	Installed []string `json:"installed"` // files written
	Skipped   []string `json:"skipped"`   // files already existing (not overwritten)
}

// InstallPrompts copies the default prompt files embedded in the binary to
// ~/.pogo/agents/. Existing files are not overwritten unless force is true.
func InstallPrompts(force bool) (*InstallResult, error) {
	if err := InitPromptDirs(); err != nil {
		return nil, err
	}

	result := &InstallResult{}
	destRoot := PromptDir()

	err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Strip the "prompts" prefix to get the relative path
		rel, _ := filepath.Rel("prompts", path)
		if rel == "." {
			return nil
		}
		destPath := filepath.Join(destRoot, rel)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		// Check if destination already exists
		if !force {
			if _, err := os.Stat(destPath); err == nil {
				result.Skipped = append(result.Skipped, rel)
				return nil
			}
		}

		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}
		result.Installed = append(result.Installed, rel)
		return nil
	})

	return result, err
}
