package driver_test

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/kinbiko/jsonassert"

	"github.com/marginalia-gaming/pogo/internal/driver"
	"github.com/marginalia-gaming/pogo/internal/project"
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
	req := url.QueryEscape("{\"type\": \"files\", \"projectRoot\": \"_testdata/a-service\"}")
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
                   "{{ .current_dir }}/_testdata/a-service/src/a.c",
                   "{{ .current_dir }}/_testdata/a-service/README.md",
                   "{{ .current_dir }}/_testdata/a-service/.gitignore"
                ]
             },
             "error":""
          }`
	templ := template.Must(template.New("Json Response").Parse(expectedResTemplate))
	err = templ.Execute(os.Stdout, map[string]interface{}{
		"current_dir": d,
	})
	if err != nil {
		t.Errorf("Error executing template %v", err)
	}
	jsonassert.New(t).Assertf(resp, expectedRes)
}
