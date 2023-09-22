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

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

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

	pluginPath := os.Getenv("POGO_PLUGIN_PATH")

	// Test if pluginPath is empty string or whitespace

	if pluginPath == "" {
		fmt.Printf("POGO_PLUGIN_PATH not set, using current directory\n")
		pluginPath, _ = os.Getwd()
	}

	paths, err := plugin.Discover("pogo*", pluginPath)
	if err != nil {
		fmt.Printf("Error discovering plugins: %v", err)
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
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   path,
		Output: os.Stdout,
		Level:  hclog.Debug,
	})
	// Start plugins
	file, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	hash := sha256.New()

	_, err = io.Copy(hash, file)
	if err != nil {
		log.Fatal(err)
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
		Logger:          logger,
		SecureConfig:    secureConfig,
	})
	clients[path] = client

	// Connect via RPC
	rpcClient, err := client.Client()
	if err != nil {
		log.Fatal(err)
	}

	// Request the plugin
	raw, err := rpcClient.Dispense("basicSearch")
	if err != nil {
		log.Fatal(err)
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
