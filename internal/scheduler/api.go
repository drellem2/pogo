package scheduler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AddRequest is the JSON body for POST /scheduler/schedules.
//
// Recurring fire:
//
//	{ "agent": "crew-research", "cron": "*/15 * * * *",
//	  "id": "research-poll", "replay_policy": "once",
//	  "delivery": "nudge", "message": "check the queue" }
//
// One-shot wakeup expressed as a duration (the daemon resolves to an absolute
// fire time on its local clock):
//
//	{ "agent": "cat-foo", "one_shot": true, "in": "30m",
//	  "delivery": "nudge", "message": "wake up" }
//
// Or with an absolute next_fire (RFC3339):
//
//	{ "agent": "cat-foo", "one_shot": true, "next_fire": "2026-05-04T09:00:00Z" }
type AddRequest struct {
	ID           string       `json:"id,omitempty"`
	Agent        string       `json:"agent"`
	Cron         string       `json:"cron,omitempty"`
	OneShot      bool         `json:"one_shot,omitempty"`
	In           string       `json:"in,omitempty"`        // e.g. "30m", "2h" — resolved to NextFire
	NextFire     time.Time    `json:"next_fire,omitempty"` // alternative to In
	ReplayPolicy ReplayPolicy `json:"replay_policy,omitempty"`
	Delivery     DeliveryMode `json:"delivery,omitempty"`
	Message      string       `json:"message,omitempty"`
}

// RegisterHandlers wires the scheduler HTTP endpoints onto mux:
//
//	GET    /scheduler/schedules           — list (filter by ?agent=)
//	POST   /scheduler/schedules           — add
//	GET    /scheduler/schedules/{id}      — fetch one
//	DELETE /scheduler/schedules/{id}      — remove
func (s *Scheduler) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/scheduler/schedules", s.handleList)
	mux.HandleFunc("/scheduler/schedules/{id}", s.handleByID)
}

func (s *Scheduler) handleList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		agent := r.URL.Query().Get("agent")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.List(agent))
	case "POST":
		var req AddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		entry, err := s.addFromRequest(req, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(entry)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func (s *Scheduler) handleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case "GET":
		entry, ok := s.Get(id)
		if !ok {
			http.Error(w, fmt.Sprintf("schedule %q not found", id), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entry)
	case "DELETE":
		removed, err := s.Remove(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !removed {
			http.Error(w, fmt.Sprintf("schedule %q not found", id), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

// addFromRequest is shared between the HTTP handler and tests so the
// request-to-entry translation has one definition.
func (s *Scheduler) addFromRequest(req AddRequest, now time.Time) (Entry, error) {
	if strings.TrimSpace(req.Agent) == "" {
		return Entry{}, fmt.Errorf("agent is required")
	}
	entry := Entry{
		ID:           req.ID,
		Agent:        req.Agent,
		Cron:         req.Cron,
		OneShot:      req.OneShot,
		NextFire:     req.NextFire,
		ReplayPolicy: req.ReplayPolicy,
		Delivery:     req.Delivery,
		Message:      req.Message,
	}
	if req.In != "" {
		dur, err := time.ParseDuration(req.In)
		if err != nil {
			return Entry{}, fmt.Errorf("invalid 'in' duration %q: %w", req.In, err)
		}
		if dur <= 0 {
			return Entry{}, fmt.Errorf("'in' duration must be positive")
		}
		entry.OneShot = true
		entry.NextFire = now.Add(dur)
	}
	return s.Add(entry, now)
}
