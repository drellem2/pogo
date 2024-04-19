////////////////////////////////////////////////////////////////////////////////
//////////////////////////// Built-in plugins //////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package driver

import (
	
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
	searchPlugin "github.com/drellem2/pogo/internal/plugins/search"
)

type BuiltinFactory func() (interface{}, error)

var builtinPlugins = map[string]BuiltinFactory{
	"pogo-plugin-search": searchPlugin.New(),
}

var builtinRegistry = newRegistry()

func newRegistry() map[string]*pogoPlugin.IPogoPlugin {
	registry := map[string]*pogoPlugin.IPogoPlugin{}
	for name, factory := range builtinPlugins {
		p, err := factory()
		if err != nil {
			logger.Error("Could not start plugin", "name", name, "err", err.Error())
		} else {
			registry[name] = p.(*pogoPlugin.IPogoPlugin)
		}		
	}
	return registry
}
