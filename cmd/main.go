////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nightlyone/lockfile"

	"github.com/marginalia-gaming/pogo/internal"
	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

// handshakeConfigs are used to just do a basic handshake between
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "SEARCH_PLUGIN",
	MagicCookieValue: "93f6bc9f97c03ed00fa85c904aca15a92752e549",
}

// pluginMap is the map of plugins we can dispense.
var pluginMap = map[string]plugin.Plugin{
	"basicSearch": &pogoPlugin.PogoPlugin{},
}

func homePage(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "greetings from pogo daemon")
	fmt.Println("Visited /")
}

func allProjects(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects")
	json.NewEncoder(w).Encode(project.Projects())
}

func file(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /file")
	switch r.Method {
	case "POST":
		decoder := json.NewDecoder(r.Body)
		var req project.VisitRequest
		decodeErr := decoder.Decode(&req)
		if decodeErr != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		response, err := project.Visit(req)
		if err != nil {
			http.Error(w, err.Message, err.Code)
			return
		}
		json.NewEncoder(w).Encode(response)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func handleRequests() {
	http.HandleFunc("/", homePage)
	http.HandleFunc("/file", file)
	http.HandleFunc("/projects", allProjects)
	fmt.Println("pogod starting")
	log.Fatal(http.ListenAndServe(":10000", nil))
}

func main() {
	// Acquire lockfile
	lock, err := lockfile.New(filepath.Join(os.TempDir(), "pogo.pid"))
	if err != nil {
		fmt.Printf("Cannot create lock. reason: %v", err)
		os.Exit(1)
	}

	if err = lock.TryLock(); err != nil {
		fmt.Printf("Cannot get lock %q, reason: %v", lock, err)
		os.Exit(1)
	}

	defer func() {
		if err := lock.Unlock(); err != nil {
			fmt.Printf("Cannot unlock %q, reason: %v", lock, err)
		}
	}()

	// Start plugins
	// Create an hclog.Logger
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin",
		Output: os.Stdout,
		Level:  hclog.Debug,
	})

	file, err := os.Open("search/search")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	hash := sha256.New()

	_, err = io.Copy(hash, file)
	if err != nil {
		log.Fatal(err)
	}

	sum := hash.Sum(nil)

	secureConfig := &plugin.SecureConfig{
		Checksum: sum,
		Hash:     sha256.New(),
	}

	// We're a host! Start by launching the plugin process.
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
		Cmd:             exec.Command("./search/search"),
		Logger:          logger,
		SecureConfig:    secureConfig,
	})
	defer client.Kill()

	// Connect via RPC
	rpcClient, err := client.Client()
	if err != nil {
		log.Fatal(err)
	}

	// Request the plugin
	raw, err := rpcClient.Dispense("basicSearch")
	if err != nil {
		log.Fatal(err)
	}
	gob.Register(pogoPlugin.ProcessProjectReq{})

	// Example plugin usage
	// basicSearch := raw.(pogoPlugin.IPogoPlugin)
	// req := pogoPlugin.ProcessProjectReq{PathVar: "abcdefghjiklmnopqrstuvwxyz"}
	// ireq := pogoPlugin.IProcessProjectReq(req)
	// err = basicSearch.ProcessProject(&ireq)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// if err != nil {
	// 	log.Fatal(err)
	// }

	defer project.SaveProjects()
	project.Init()
	handleRequests()
}
