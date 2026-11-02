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
	"strings"
	"sync/atomic"

	"github.com/drellem2/pogo/internal/config"
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
	if IsSearchPlugin(path) && !SearchEnabled() {
		// Never fall through to checkAlive: for a path it has not loaded it
		// exec's the path as an external plugin binary.
		return nil
	}
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

// searchEnabled gates the file-search plugin. Off unless pogod calls
// SetSearchEnabled(true) from [search] enabled (mg-cabf4). Read by Init, the
// /plugin handler and GetPlugin; set once at startup before Init.
var searchEnabled atomic.Bool

// SetSearchEnabled records whether the file-search plugin may be loaded. Call
// it before Init: Init is what leaves the plugin out of Interfaces, which is
// what keeps it out of /plugins and out of every ProcessProject fan-out (the
// startup index pass and `pogo visit` registration).
func SetSearchEnabled(enabled bool) {
	searchEnabled.Store(enabled)
}

// SearchEnabled reports whether the file-search plugin is enabled.
func SearchEnabled() bool {
	return searchEnabled.Load()
}

// IsSearchPlugin reports whether path names the file-search plugin, built-in
// or an external binary of the same name. It matches the way clients find it
// (a substring match on SearchPluginName).
func IsSearchPlugin(path string) bool {
	return strings.Contains(filepath.Base(path), SearchPluginName)
}

func Init() {
	gob.Register(pogoPlugin.ProcessProjectReq{})
	clients = make(map[string]*plugin.Client)
	Interfaces = make(map[string]*pogoPlugin.IPogoPlugin)

	if pluginPath := resolvePluginPath(); pluginPath != "" {
		discoverExternalPlugins(pluginPath)
	}

	for name, plugin := range builtinRegistry {
		if name == SearchPluginName && !SearchEnabled() {
			logger.Info("search indexing disabled; not loading plugin", "name", name)
			continue
		}
		if Interfaces[name] != nil {
			logger.Debug("Found runtime copy of plugin, skipping builtin", "name", name)
		} else {
			Interfaces[name] = plugin
		}
	}
}

// resolvePluginPath returns the directory to scan for external plugins.
// Order: $POGO_PLUGIN_PATH, then $POGO_HOME/plugin (default ~/.pogo/plugin
// via config.PogoHome).
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
	return filepath.Join(config.PogoHome(), "plugin")
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
		if IsSearchPlugin(path) && !SearchEnabled() {
			logger.Info("search indexing disabled; not starting external plugin", "path", path)
			continue
		}
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
