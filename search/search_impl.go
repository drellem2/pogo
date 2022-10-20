package main

import (
	"encoding/gob"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

type BasicSearch struct {
	logger hclog.Logger
}

// API Version for this plugin
const version = "0.0.1"

func (g *BasicSearch) Info() *pogoPlugin.PluginInfoRes {
	g.logger.Debug("Returning version %s", version)
	return &pogoPlugin.PluginInfoRes{Version: version}
}

func (g *BasicSearch) ProcessProject(req *pogoPlugin.IProcessProjectReq) error {
	g.logger.Debug("Processing project %s", (*req).Path())
	return nil
}

// handshakeConfigs are used to just do a basic handshake between
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "SEARCH_PLUGIN",
	MagicCookieValue: "93f6bc9f97c03ed00fa85c904aca15a92752e549",
}

func main() {
	gob.Register(pogoPlugin.ProcessProjectReq{})
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Trace,
		Output:     os.Stderr,
		JSONFormat: true,
	})

	basicSearch := &BasicSearch{
		logger: logger,
	}
	// pluginMap is the map of plugins we can dispense.
	var pluginMap = map[string]plugin.Plugin{
		"basicSearch": &pogoPlugin.PogoPlugin{Impl: basicSearch},
	}

	logger.Debug("message from plugin", "foo", "bar")

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
	})
}
