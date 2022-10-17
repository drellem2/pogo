////////////////////////////////////////////////////////////////////////////////
////////// Plugin driver                                              //////////
////////////////////////////////////////////////////////////////////////////////

package driver

import (
	"crypto/sha256"
	"encoding/gob"
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
	ProtocolVersion:  1,
	MagicCookieKey:   "SEARCH_PLUGIN",
	MagicCookieValue: "93f6bc9f97c03ed00fa85c904aca15a92752e549",
}

// pluginMap is the map of plugins we can dispense.
var pluginMap = map[string]plugin.Plugin{
	"basicSearch": &pogoPlugin.PogoPlugin{},
}

var client *plugin.Client

func Init() *plugin.Client {
	// Start plugins
	// Create an hclog.Logger
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin",
		Output: os.Stdout,
		Level:  hclog.Debug,
	})

	file, err := os.Open("search/search")
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

	gob.Register(pogoPlugin.ProcessProjectReq{})

	// We're a host! Start by launching the plugin process.
	client = plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
		Cmd:             exec.Command("./search/search"),
		Logger:          logger,
		SecureConfig:    secureConfig,
	})
	return client
}

// Clean up, kills all plugins
func Kill() {
	client.Kill()
}
