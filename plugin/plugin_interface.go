package plugin

import (
	"net/rpc"

	"github.com/hashicorp/go-plugin"
)

type IProcessProjectReq interface {
	Path() string
}

type ProcessProjectReq struct {
	PathVar string
}

func (r ProcessProjectReq) Path() string {
	return r.PathVar
}

// The interface that plugins should implement
type IPogoPlugin interface {
	// Notifies the plugin that a project exists. It is the plugin's responsibility
	// to decide when and to what extent action should be taken.
	ProcessProject(req *IProcessProjectReq) error
}

// Here is an implementation that talks over RPC
type PluginRPC struct{ client *rpc.Client }

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
