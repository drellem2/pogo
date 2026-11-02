////////////////////////////////////////////////////////////////////////////////
//////////////////////////// Built-in plugins //////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package driver

import (
	diagnosticsPlugin "github.com/drellem2/pogo/internal/diagnostics"
	searchPlugin "github.com/drellem2/pogo/internal/search"
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

type BuiltinFactory func() (pogoPlugin.IPogoPlugin, error)

// SearchPluginName is the built-in file-search plugin's registry name — the
// string /plugins returns and every search client looks for.
const SearchPluginName = "pogo-plugin-search"

var builtinPlugins = map[string]BuiltinFactory{
	SearchPluginName:          searchPlugin.New(),
	"pogo-plugin-diagnostics": diagnosticsPlugin.New(),
}

var builtinRegistry = newRegistry()

func newRegistry() map[string]*pogoPlugin.IPogoPlugin {
	registry := map[string]*pogoPlugin.IPogoPlugin{}
	for name, factory := range builtinPlugins {
		p, err := factory()
		if err != nil {
			logger.Error("Could not start plugin", "name", name, "err", err.Error())
		} else {
			registry[name] = &p
		}
	}
	return registry
}
