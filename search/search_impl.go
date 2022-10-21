package main

import (
	"encoding/gob"
	"net/url"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

type BasicSearch struct {
	logger   hclog.Logger
	projects map[string]IndexedProject
}

// API Version for this plugin
const version = "0.0.1"

func (g *BasicSearch) Info() *pogoPlugin.PluginInfoRes {
	g.logger.Debug("Returning version %s", version)
	return &pogoPlugin.PluginInfoRes{Version: version}
}

// Just a dummy function for now
func (g *BasicSearch) Execute(req string) string {
	g.logger.Debug("Executing request.")
	return url.QueryEscape("{ \"value\": true}")
}

func (g *BasicSearch) ProcessProject(req *pogoPlugin.IProcessProjectReq) error {
	g.logger.Debug("Processing project %s", (*req).Path())
	g.Index(req)
	return nil
}

// handshakeConfigs are used to just do a basic handshake betw1een
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  2,
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
		logger:   logger,
		projects: make(map[string]IndexedProject),
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
