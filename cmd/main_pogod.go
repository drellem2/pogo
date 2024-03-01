////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/project"

	pogoPlugin "github.com/drellem2/pogo/plugin"
)

func health(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /health")
	fmt.Fprintf(w, "pogo is up and bouncing")
}

func homePage(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /")
	fmt.Fprintf(w, "greetings from pogo daemon")
}

func allProjects(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects")
	json.NewEncoder(w).Encode(project.Projects())
}

func clean(path string) string {
	// Append a trailing delimiter if it doesn't exist
	p := filepath.Clean(path)
	if p[len(p)-1] != filepath.Separator {
		p += string(filepath.Separator)
	}
	return p
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
		req.Path = clean(req.Path)
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

func plugin(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugin")
	switch r.Method {
	case "GET":
		encodedPath := r.URL.Query().Get("path")
		path, err := url.QueryUnescape(encodedPath)
		path = clean(path)
		if err != nil {
			fmt.Printf("Error urldecoding path variable: %v\n", err)
			return
		}
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		resp := (*plugin).Info()
		json.NewEncoder(w).Encode(resp)
		return
	case "POST":
		var reqObj pogoPlugin.DataObject
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&reqObj)
		if err != nil {
			fmt.Printf("Request could not be parsed.")
			http.Error(w, "", http.StatusBadRequest)
			return
		}

		path := reqObj.Plugin
		path = clean(path)
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		respString := (*plugin).Execute(reqObj.Value)
		var respObj = pogoPlugin.DataObject{Value: respString}
		json.NewEncoder(w).Encode(respObj)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugins(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugins")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(driver.GetPluginPaths())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func handleRequests() {
	http.HandleFunc("/", homePage)
	http.HandleFunc("/file", file)
	http.HandleFunc("/projects", allProjects)
	http.HandleFunc("/plugin", plugin)
	http.HandleFunc("/plugins", plugins)
	http.HandleFunc("/health", health)
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
	driver.Init()

	defer driver.Kill()
	defer project.SaveProjects()
	project.Init()
	handleRequests()
}
