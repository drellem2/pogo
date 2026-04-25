package refinery

import (
	"encoding/json"
	"net/http"
)

// DisabledMessage is the user-facing error for write operations when the
// refinery is disabled in pogod's config.
const DisabledMessage = "refinery is disabled in pogod config (set [refinery] enabled = true in ~/.config/pogo/config.toml to enable)"

// RegisterDisabledHandlers wires up /refinery/* endpoints that respond
// politely when the refinery is disabled. Read-only endpoints return empty
// data with enabled=false so dashboards keep working; write endpoints
// (submit, cancel, prune) fail fast with a 503 and a helpful message.
func RegisterDisabledHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/refinery/status", handleDisabledStatus)
	mux.HandleFunc("/refinery/queue", handleDisabledList)
	mux.HandleFunc("/refinery/history", handleDisabledList)
	mux.HandleFunc("/refinery/submit", handleDisabledWrite)
	mux.HandleFunc("/refinery/cancel", handleDisabledWrite)
	mux.HandleFunc("/refinery/prune", handleDisabledWrite)
	mux.HandleFunc("/refinery/mr/{id}", handleDisabledMR)
}

func handleDisabledStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Status{
		Enabled: false,
		Running: false,
	})
}

func handleDisabledList(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("[]\n"))
}

func handleDisabledWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	http.Error(w, DisabledMessage, http.StatusServiceUnavailable)
}

func handleDisabledMR(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	http.Error(w, DisabledMessage, http.StatusNotFound)
}
