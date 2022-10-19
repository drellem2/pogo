////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nightlyone/lockfile"

	"github.com/marginalia-gaming/pogo/internal/driver"
	"github.com/marginalia-gaming/pogo/internal/project"
)

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

// TODO: Add new endpoints for retrieving/calling plugin endpoints
// Maybe schema just returns an api version and the client verifies it?
// Everything else is just the client writing.
// Example:
// - GET /plugins - all plugins
// - GET /plugin/#{name} plugin api version and any other details
// - POST /plugin/#{name} submit request to plugin api
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
	driver.Init()

	defer driver.Kill()
	defer project.SaveProjects()
	project.Init()
	handleRequests()
}
