////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
    "encoding/json"
    "fmt"
    "log"
    "net/http"

    "github.com/marginalia-gaming/pogo/internal"
)

func homePage(w http.ResponseWriter, r *http.Request){
    fmt.Fprintf(w, "greetings from pogo daemon")
    fmt.Println("Endpoint Hit: homePage")
}

func allProjects(w http.ResponseWriter, r *http.Request){
	fmt.Println("Endpoint Hit: allProjects")
    json.NewEncoder(w).Encode(project.Projects())
}

func file(w http.ResponseWriter, r *http.Request){
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
    project.Init()

	handleRequests()
}
