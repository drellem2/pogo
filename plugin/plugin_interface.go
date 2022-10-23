package plugin

import (
	"fmt"
	"net/rpc"

	"github.com/hashicorp/go-plugin"
)

type DataObject struct {
	Plugin string `json:"plugin"`
	Value  string `json:"value"`
}

type IProcessProjectReq interface {
	Path() string
}

type ProcessProjectReq struct {
	PathVar string
}

func (r ProcessProjectReq) Path() string {
	return r.PathVar
}

type PluginInfoRes struct {
	Version string `json:"version"`
}

// The interface that plugins should implement
type IPogoPlugin interface {
	// Returns info about the plugin. Most importantly API version.
	Info() *PluginInfoRes

	// Executes a url-encoded json string, and returns one.
	Execute(req string) string

	// Notifies the plugin that a project exists. It is the plugin's responsibility
	// to decide when and to what extent action should be taken.
	ProcessProject(req *IProcessProjectReq) error
}

// Here is an implementation that talks over RPC
type PluginRPC struct{ client *rpc.Client }

func (g *PluginRPC) Info() *PluginInfoRes {
	var resp *PluginInfoRes
	err := g.client.Call("Plugin.Info", new(interface{}), &resp)
	if err != nil {
		fmt.Printf("Error finding plugin info: %v", err)
		return nil
	}
	return resp
}

func (g *PluginRPC) Execute(req string) string {
	var resp string
	err := g.client.Call("Plugin.Execute", req, &resp)
	if err != nil {
		fmt.Printf("Error executing plugin call: %v", err)
		return ""
	}
	return resp
}

func (g *PluginRPC) ProcessProject(req *IProcessProjectReq) error {
	var resp error
	err := g.client.Call("Plugin.ProcessProject", req, &resp)
	if err != nil {
		return err
	}

	return resp
}

// Here is the RPC server that PluginRPC talks to, conforming to
// the requirements of net/rpc
type PluginRPCServer struct {
	// This is the real implementation
	Impl IPogoPlugin
}

func (s *PluginRPCServer) Info(args interface{}, resp **PluginInfoRes) error {
	*resp = s.Impl.Info()
	return nil
}

func (s *PluginRPCServer) Execute(req string, resp *string) error {
	*resp = s.Impl.Execute(req)
	return nil
}

func (s *PluginRPCServer) ProcessProject(args interface{}, resp *error) error {
	var iargs = args.(IProcessProjectReq)
	*resp = s.Impl.ProcessProject(&iargs)
	return nil
}

// This is the implementation of plugin.Plugin so we can serve/consume this
//
// This has two methods: Server must return an RPC server for this plugin
// type. We construct a PluginRPCServer for this.
//
// Client must return an implementation of our interface that communicates
// over an RPC client. We return PluginRPC for this.
//
// Ignore MuxBroker. That is used to create more multiplexed streams on our
// plugin connection and is a more advanced use case.
type PogoPlugin struct {
	// Impl Injection
	Impl IPogoPlugin
}

func (p *PogoPlugin) Server(*plugin.MuxBroker) (interface{}, error) {
	return &PluginRPCServer{Impl: p.Impl}, nil
}

func (PogoPlugin) Client(b *plugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &PluginRPC{client: c}, nil
}
