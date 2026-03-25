package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPort = 10000
	DefaultBind = "127.0.0.1"
)

// RunMode represents the operating mode of the pogo daemon.
type RunMode int

const (
	// ModeFull means everything is running: agents, refinery, indexing, HTTP.
	ModeFull RunMode = iota
	// ModeIndexOnly means only project indexing, search, and HTTP are running.
	// Agents and refinery are stopped.
	ModeIndexOnly
)

// String returns the human-readable name of the run mode.
func (m RunMode) String() string {
	switch m {
	case ModeFull:
		return "full"
	case ModeIndexOnly:
		return "index-only"
	default:
		return "unknown"
	}
}

// DefaultAgentCommand is the default command template for spawning agents.
const DefaultAgentCommand = "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}"

// DefaultMaxWatchers is the default cap on filesystem watchers.
// macOS kqueue uses one file descriptor per watched path; too many watchers
// can exhaust the per-process FD limit and prevent child process creation.
const DefaultMaxWatchers = 4096

// Config holds pogo daemon configuration.
type Config struct {
	Port        int
	Bind        string
	MaxWatchers int
	Refinery    RefineryConfig
	Agents      AgentsConfig
}

// AgentsConfig holds agent command configuration.
type AgentsConfig struct {
	// Command is the default command template for all agent types.
	// Supports Go template variables: {{.PromptFile}}, {{.AgentName}}, {{.AgentType}}, {{.WorkDir}}
	Command string
	// Crew overrides the command template for crew agents.
	Crew AgentTypeConfig
	// Polecat overrides the command template for polecat agents.
	Polecat AgentTypeConfig
}

// AgentTypeConfig holds per-agent-type command configuration.
type AgentTypeConfig struct {
	Command string
}

// AgentCommand returns the command template for a given agent type,
// falling back to the default if no per-type override is set.
func (c *AgentsConfig) AgentCommand(agentType string) string {
	switch agentType {
	case "crew":
		if c.Crew.Command != "" {
			return c.Crew.Command
		}
	case "polecat":
		if c.Polecat.Command != "" {
			return c.Polecat.Command
		}
	}
	if c.Command != "" {
		return c.Command
	}
	return DefaultAgentCommand
}

// RefineryConfig holds merge queue configuration.
type RefineryConfig struct {
	Enabled      bool
	PollInterval time.Duration
}

// Load reads configuration from (in priority order):
//  1. POGO_PORT environment variable
//  2. ~/.config/pogo/config.toml [server] port field
//  3. Default (10000)
func Load() *Config {
	cfg := &Config{
		Port:        DefaultPort,
		Bind:        DefaultBind,
		MaxWatchers: DefaultMaxWatchers,
		Refinery: RefineryConfig{
			Enabled:      true,
			PollInterval: 30 * time.Second,
		},
	}

	// Try config file first (lowest priority, overridden by env)
	if fileCfg, err := loadConfigFile(); err == nil {
		if fileCfg.Port != 0 {
			cfg.Port = fileCfg.Port
		}
		if fileCfg.Bind != "" {
			cfg.Bind = fileCfg.Bind
		}
		if fileCfg.MaxWatchers > 0 {
			cfg.MaxWatchers = fileCfg.MaxWatchers
		}
		cfg.Agents = fileCfg.Agents
	}

	// Environment variables override config file
	if portStr := os.Getenv("POGO_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			cfg.Port = port
		}
	}
	if bind := os.Getenv("POGO_BIND"); bind != "" {
		cfg.Bind = bind
	}
	if mwStr := os.Getenv("POGO_MAX_WATCHERS"); mwStr != "" {
		if mw, err := strconv.Atoi(mwStr); err == nil && mw > 0 {
			cfg.MaxWatchers = mw
		}
	}

	// POGO_AGENT_COMMAND overrides the default agent command from config file
	if agentCmd := os.Getenv("POGO_AGENT_COMMAND"); agentCmd != "" {
		cfg.Agents.Command = agentCmd
	}

	return cfg
}

// ServerURL returns the base URL for connecting to the pogo daemon.
func (c *Config) ServerURL() string {
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

// ListenAddr returns the address string for the server to listen on.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.Port)
}

// ConfigDir returns the pogo configuration directory path.
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pogo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pogo")
}

// ConfigFilePath returns the path to the pogo config file.
func ConfigFilePath() string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// loadConfigFile reads port from the TOML config file.
// Only parses the minimal subset needed: [server] section with port key.
func loadConfigFile() (*Config, error) {
	path := ConfigFilePath()
	if path == "" {
		return nil, fmt.Errorf("no config path")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{}
	currentSection := ""
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") {
			currentSection = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Strip surrounding quotes from values
		unquotedVal := strings.Trim(val, "\"")

		switch currentSection {
		case "server":
			switch key {
			case "port":
				if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
					cfg.Port = port
				}
			case "bind":
				cfg.Bind = val
			}
		case "refinery":
			switch key {
			case "enabled":
				cfg.Refinery.Enabled = val == "true"
			case "poll_interval":
				val = strings.Trim(val, "\"")
				if d, err := time.ParseDuration(val); err == nil {
					cfg.Refinery.PollInterval = d
				}
			}
		case "search":
			switch key {
			case "max_watchers":
				if mw, err := strconv.Atoi(val); err == nil && mw > 0 {
					cfg.MaxWatchers = mw
				}
			}
		case "agents":
			if key == "command" {
				cfg.Agents.Command = unquotedVal
			}
		case "agents.crew":
			if key == "command" {
				cfg.Agents.Crew.Command = unquotedVal
			}
		case "agents.polecat":
			if key == "command" {
				cfg.Agents.Polecat.Command = unquotedVal
			}
		}
	}

	return cfg, scanner.Err()
}
