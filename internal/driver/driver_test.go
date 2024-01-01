package driver_test

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"text/template"
	"time"

	"github.com/kinbiko/jsonassert"

	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/project"
)

const aService = "_testdata/a-service/" // In initial state
const bService = "_testdata/b-service/" // Not in initial state
const zService = "_testdata/z-service/" // Doesn't exist

const readme = "/README.md"
const mainC = "/src/main.c"

func init() {
	os.Chdir("../..")
}

func absolute(path string) (string, error) {
	str, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err2 := os.Lstat(path)
	if err2 != nil {
		return "", err2
	}
	if info.IsDir() {
		return str + "/", nil
	}
	return str, nil
}

func setUp(t *testing.T) (string, error) {
	d, _ := os.Getwd()
	t.Logf("Current working directory: %s", d)
	aServiceAbs, err := absolute(aService)
	if err != nil {
		return "", err
	}
	t.Logf("a-service at: %s", aServiceAbs)
	driver.Init()
	project.ProjectFileName = "projects-test.json"
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
	project.RemoveSaveFile()
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

func TestPluginRestarts(t *testing.T) {
	t.Logf("Starting test TestPluginRestarts")
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
	pluginClient := driver.GetPluginClient(pluginPath)
	pluginClient.Kill()
	if !pluginClient.Exited() {
		t.Errorf("Failed to kill plugin")
		return
	}
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
	// Initialization causes the search plugin to index the files.
	// We wait for that goroutine to finish before executing the test.
	time.Sleep(1 * time.Second)

	plugins := driver.GetPluginPaths()
	numPlugins := len(plugins)
	if numPlugins < 1 {
		t.Errorf("Wrong number of plugins, expected at least %d but found %d", 1, numPlugins)
		return
	}
	pluginPath := plugins[0]
	plugin := driver.GetPlugin(pluginPath)
	aServiceAbs, err := absolute(aService)
	if err != nil {
		t.Errorf("Failed to get absolute path for %s", aService)
		return
	}

	req := url.QueryEscape("{\"type\": \"files\", \"projectRoot\": \"" + aServiceAbs + "\"}")
	encodedResp := (*plugin).Execute(req)
	resp, err2 := url.QueryUnescape(encodedResp)
	t.Logf("Response: %s", resp)
	if err2 != nil {
		t.Errorf("Error decoding response %v", err2)
	}
	// Print current directory
	d, _ := os.Getwd()
	expectedResTemplate := `
          {
            "index":{
               "root":"{{ .current_dir }}/_testdata/a-service/",
               "paths":[
                  "src/a.c",
                  "README.md",
                  ".gitignore"
               ]
            },
            "results":{
               "files":null
            },
            "error":""
         }`
	var buff bytes.Buffer
	templ := template.Must(template.New("Json Response").Parse(expectedResTemplate))
	err = templ.Execute(&buff, map[string]interface{}{
		"current_dir": d,
	})
	if err != nil {
		t.Errorf("Error executing template %v", err)
	}
	jsonassert.New(t).Assertf(resp, buff.String())
}
