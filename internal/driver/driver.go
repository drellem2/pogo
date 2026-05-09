////////////////////////////////////////////////////////////////////////////////
////////// Plugin driver                                              //////////
////////////////////////////////////////////////////////////////////////////////

package driver

import (
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

var logger = hclog.New(&hclog.LoggerOptions{
	Name:   "driver",
	Output: os.Stdout,
	Level:  hclog.Debug,
})

// handshakeConfigs are used to just do a basic handshake between
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  2,
	MagicCookieKey:   "SEARCH_PLUGIN",
	MagicCookieValue: "93f6bc9f97c03ed00fa85c904aca15a92752e549",
}

// pluginMap is the map of plugins we can dispense.
var pluginMap = map[string]plugin.Plugin{
	"basicSearch": &pogoPlugin.PogoPlugin{},
	"diagnostics": &pogoPlugin.PogoPlugin{},
}

var clients map[string]*plugin.Client
var Interfaces map[string]*pogoPlugin.IPogoPlugin

type PluginManager struct {
}

type PluginInfoReq struct {
	Path string `json:"path"`
}

func GetPluginManager() *PluginManager {
	return &PluginManager{}
}

func GetPluginPaths() []string {
	keys := make([]string, len(Interfaces))
	i := 0
	for k := range Interfaces {
		keys[i] = k
		i++
	}
	return keys
}

func (g *PluginManager) Info() *pogoPlugin.PluginInfoRes {
	return &pogoPlugin.PluginInfoRes{Version: ""}
}

func GetPluginClient(path string) *plugin.Client {
	return clients[path]
}

func GetPlugin(path string) *pogoPlugin.IPogoPlugin {
	checkAlive(path)
	return Interfaces[path]
}

func checkAlive(path string) {
	if Interfaces[path] == nil {
		startPlugin(path)
		return
	}

	// Built-in plugin
	if clients[path] == nil {
		return
	}

	// Check if plugin is alive
	if clients[path].Exited() {
		// Plugin is dead, restart
		startPlugin(path)
	}
}

func (g *PluginManager) ProcessProject(req *pogoPlugin.IProcessProjectReq) error {
	var err error
	hasErr := false
	for path, _ := range Interfaces {
		checkAlive(path)
	}
	for _, raw := range Interfaces {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("Caught error: %v", r)
					hasErr = true
				}
			}()
			basicSearch := pogoPlugin.IPogoPlugin(*raw)
			err = basicSearch.ProcessProject(req)
			if err != nil {
				fmt.Printf("Caught error calling ProcessProject(): %v", err)
			}
		}()
	}
	if hasErr {
		return errors.New("Error calling ProcessProject")
	}
	return nil
}

func Init() {
	gob.Register(pogoPlugin.ProcessProjectReq{})
	clients = make(map[string]*plugin.Client)
	Interfaces = make(map[string]*pogoPlugin.IPogoPlugin)

	if pluginPath := resolvePluginPath(); pluginPath != "" {
		discoverExternalPlugins(pluginPath)
	}

	for name, plugin := range builtinRegistry {
		if Interfaces[name] != nil {
			logger.Debug("Found runtime copy of plugin, skipping builtin", "name", name)
		} else {
			Interfaces[name] = plugin
		}
	}
}

// resolvePluginPath returns the directory to scan for external plugins.
// Order: $POGO_PLUGIN_PATH, then $POGO_HOME/plugin, then ~/.pogo/plugin.
// Returns "" if none can be resolved (e.g. unreadable home dir) — the caller
// should skip external discovery and rely on builtins.
//
// Pre-mg-b08c this fell back to cwd, which made `pogod` load whatever pogo*
// binaries lived next to wherever it was launched. Foot-gun once pogod is
// invoked from agent worktrees and the pogo source tree itself.
func resolvePluginPath() string {
	if env := os.Getenv("POGO_PLUGIN_PATH"); env != "" {
		abs, err := filepath.Abs(env)
		if err != nil {
			fmt.Printf("Error getting absolute path for POGO_PLUGIN_PATH=%q: %v\n", env, err)
			return ""
		}
		return abs
	}
	if home := os.Getenv("POGO_HOME"); home != "" {
		return filepath.Join(home, "plugin")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".pogo", "plugin")
}

func discoverExternalPlugins(pluginPath string) {
	if _, err := os.Stat(pluginPath); err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("Cannot stat plugin dir %s: %v\n", pluginPath, err)
		}
		return
	}
	paths, err := plugin.Discover("pogo*", pluginPath)
	if err != nil {
		fmt.Printf("Error discovering plugins: %v\n", err)
		return
	}
	fmt.Printf("Discovered %d plugins in dir %s: %v\n", len(paths), pluginPath, paths)
	for _, path := range paths {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("Caught error during plugin creation: %v", r)
				}
			}()
			startPlugin(path)
		}()
	}
}

func startPlugin(path string) {
	// Create an hclog.Logger
	pluginLogger := hclog.New(&hclog.LoggerOptions{
		Name:   path,
		Output: os.Stdout,
		Level:  hclog.Debug,
	})
	// Start plugins
	file, err := os.Open(path)
	if err != nil {
		logger.Error("Skipping plugin: cannot open", "path", path, "err", err)
		return
	}
	defer file.Close()

	hash := sha256.New()

	if _, err = io.Copy(hash, file); err != nil {
		logger.Error("Skipping plugin: cannot hash", "path", path, "err", err)
		return
	}

	sum := hash.Sum(nil)

	secureConfig := &plugin.SecureConfig{
		Checksum: sum,
		Hash:     sha256.New(),
	}

	// We're a host! Start by launching the plugin process.
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
		Cmd:             exec.Command(path),
		Logger:          pluginLogger,
		SecureConfig:    secureConfig,
	})
	clients[path] = client

	// Connect via RPC
	rpcClient, err := client.Client()
	if err != nil {
		logger.Error("Skipping plugin: rpc client failed", "path", path, "err", err)
		client.Kill()
		delete(clients, path)
		return
	}

	// Request the plugin
	raw, err := rpcClient.Dispense("basicSearch")
	if err != nil {
		logger.Error("Skipping plugin: dispense failed", "path", path, "err", err)
		client.Kill()
		delete(clients, path)
		return
	}
	praw := raw.(pogoPlugin.IPogoPlugin)
	Interfaces[path] = &praw
}

// Clean up, kills all plugins
func Kill() {
	for _, client := range clients {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Caught error during plugin termination: %v", r)
				}
			}()
			if client != nil {
				client.Kill()
			}
		}()
	}
}
