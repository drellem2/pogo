package driver_test

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/marginalia-gaming/pogo/internal/driver"
	"github.com/marginalia-gaming/pogo/internal/project"
)

const aService = "_testdata/a-service" // In initial state
const bService = "_testdata/b-service" // Not in initial state
const zService = "_testdata/z-service" // Doesn't exist

const readme = "/README.md"
const mainC = "/src/main.c"

func init() {
	os.Chdir("../..")
}

func setUp(t *testing.T) (string, error) {
	d, _ := os.Getwd()
	t.Logf("Current working directory: %s", d)
	aServiceAbs, err := filepath.Abs(aService)
	if err != nil {
		return "", err
	}
	t.Logf("a-service at: %s", aServiceAbs)
	driver.Init()
	project.Init()
	p := project.Project{
		Id:   0,
		Path: aServiceAbs,
	}
	project.Add(&p)
	return aServiceAbs, nil
}

func cleanUp() {
	driver.Kill()
}

func TestPluginsLoad(t *testing.T) {
	t.Logf("Starting test TestPluginsLoad")
	_, err := setUp(t)
	defer cleanUp()
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	numPlugins := len(driver.GetPluginPaths())
	if numPlugins != 1 {
		t.Errorf("Wrong number of plugins, expected %d but found %d", 1, numPlugins)
		return
	}
}

func TestPluginInfo(t *testing.T) {
	t.Logf("Starting test TestPluginInfo")
	_, err := setUp(t)
	defer cleanUp()
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	plugins := driver.GetPluginPaths()
	numPlugins := len(plugins)
	if numPlugins < 1 {
		t.Errorf("Wrong number of plugins, expected at least %d but found %d", 1, numPlugins)
		return
	}
	pluginPath := plugins[0]
	plugin := driver.GetPlugin(pluginPath)
	if plugin == nil {
		t.Errorf("Unexpected nil plugin")
		return
	}
	info := (*plugin).Info()
	if info == nil {
		t.Errorf("Unexpected nil info")
		return
	}
	version := (*info).Version
	if version != "0.0.1" {
		t.Errorf("Unexpected version %s expected %s", version, "0.0.1")
		return
	}
}

func TestPluginExecute(t *testing.T) {
	t.Logf("Starting test TestPluginExecute")
	_, err := setUp(t)
	defer cleanUp()
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	plugins := driver.GetPluginPaths()
	numPlugins := len(plugins)
	if numPlugins < 1 {
		t.Errorf("Wrong number of plugins, expected at least %d but found %d", 1, numPlugins)
		return
	}
	pluginPath := plugins[0]
	plugin := driver.GetPlugin(pluginPath)
	req := url.QueryEscape("{}")
	resp := (*plugin).Execute(req)
	expectedRes := url.QueryEscape("{ \"value\": true}")
	if resp != expectedRes {
		t.Errorf("Unexpected response %s expected %s", resp, expectedRes)
		return
	}
}
