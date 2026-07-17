package driver_test

import (
	"bytes"
	"encoding/json"
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
	if numPlugins != 2 {
		t.Errorf("Wrong number of plugins, expected %d but found %d", 2, numPlugins)
		return
	}
}

// TestInitIgnoresCwdWithPogoBinaries guards mg-b08c: when POGO_PLUGIN_PATH is
// unset, Init() must NOT scan the current working directory, even if cwd
// contains files matching the "pogo*" glob (as happens when the daemon is run
// from the pogo source tree itself, ~/DUGLocal/pogo/, or any agent worktree).
// Pre-fix, this would have tried to launch every pogo* binary in cwd as a
// plugin, log.Fatal'ing the daemon on the first failure.
func TestInitIgnoresCwdWithPogoBinaries(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"pogo-bait", "pogod-bait", "pogo-plugin-bait"} {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte("not a real plugin"), 0755); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	prevDir, _ := os.Getwd()
	defer os.Chdir(prevDir)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}

	t.Setenv("POGO_PLUGIN_PATH", "")
	t.Setenv("POGO_HOME", filepath.Join(tmp, "no-such-pogo-home"))

	driver.Init()
	defer driver.Kill()

	// Only the two builtins should be registered. The pogo*-bait files in cwd
	// must be ignored — discovering them would mean the cwd-fallback regressed.
	paths := driver.GetPluginPaths()
	if len(paths) != 2 {
		t.Errorf("expected 2 builtin plugins, got %d: %v", len(paths), paths)
	}
	for _, p := range paths {
		if filepath.Dir(p) == tmp {
			t.Errorf("plugin %q discovered from cwd — cwd-fallback regression", p)
		}
	}
}

// Test doesn't work for builtin plugins

// func TestPluginRestarts(t *testing.T) {
// 	t.Logf("Starting test TestPluginRestarts")
// 	_, err := setUp(t)
// 	defer cleanUp()
// 	if err != nil {
// 		t.Errorf("Failed test set-up %v", err)
// 		return
// 	}
// 	plugins := driver.GetPluginPaths()
// 	numPlugins := len(plugins)
// 	if numPlugins < 1 {
// 		t.Errorf("Wrong number of plugins, expected at least %d but found %d", 1, numPlugins)
// 		return
// 	}
// 	pluginPath := plugins[0]
// 	pluginClient := driver.GetPluginClient(pluginPath)
// 	pluginClient.Kill()
// 	if !pluginClient.Exited() {
// 		t.Errorf("Failed to kill plugin")
// 		return
// 	}
// 	plugin := driver.GetPlugin(pluginPath)
// 	if plugin == nil {
// 		t.Errorf("Unexpected nil plugin")
// 		return
// 	}
// 	info := (*plugin).Info()
// 	if info == nil {
// 		t.Errorf("Unexpected nil info")
// 		return
// 	}
// 	version := (*info).Version
// 	if version != "0.0.1" {
// 		t.Errorf("Unexpected version %s expected %s", version, "0.0.1")
// 		return
// 	}
// }

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
	searchPlugin := driver.GetPlugin("pogo-plugin-search")
	if searchPlugin == nil {
		t.Errorf("Search plugin not found")
		return
	}
	aServiceAbs, err := absolute(aService)
	if err != nil {
		t.Errorf("Failed to get absolute path for %s", aService)
		return
	}

	req := "{\"type\": \"files\", \"projectRoot\": \"" + aServiceAbs + "\"}"

	// Initialization kicks off asynchronous indexing in a background
	// goroutine (search.ProcessProject spawns `go Index`). The project lands
	// in the index map as "indexing" and only later flips to "ready" with
	// git_tree_hash populated. Reading $.index immediately after the first
	// Execute therefore races the indexer and intermittently fails the
	// assertion with indexing_status=="indexing" and git_tree_hash missing
	// (mg-9ddd). Poll the "files" request until the index reports "ready" —
	// or a timeout elapses — before asserting on the final response.
	var resp string
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp = (*searchPlugin).Execute(req)
		var probe struct {
			Index struct {
				Status string `json:"indexing_status"`
			} `json:"index"`
		}
		if jsonErr := json.Unmarshal([]byte(resp), &probe); jsonErr == nil &&
			probe.Index.Status == "ready" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Errorf("index never reached indexing_status=ready within timeout; last response: %s", resp)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("Response: %s", resp)
	// Print current directory
	d, _ := os.Getwd()
	expectedResTemplate := `
          {
            "index":{
               "root":"{{ .current_dir }}/_testdata/a-service/",
               "paths":[
                  "<<UNORDERED>>",
                  "src/a.c",
                  "README.md",
                  ".gitignore"
               ],
               "file_hashes":"<<PRESENCE>>",
               "file_mtimes":"<<PRESENCE>>",
               "git_tree_hash":"<<PRESENCE>>",
               "indexing_status":"ready"
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
	jsonassert.New(t).Assertf(resp, "%s", buff.String())
}
