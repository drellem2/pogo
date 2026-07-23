package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
//	GET    /scheduler/schedules                — list (filter by ?agent=)
//	POST   /scheduler/schedules                — add
//	GET    /scheduler/schedules/{id}[?agent=X] — fetch one
//	DELETE /scheduler/schedules/{id}[?agent=X] — remove
//
// Schedules are keyed on (agent, id), so two agents may register the same
// id without collision. The {id} endpoints accept an optional ?agent=X query
// param: with it, the lookup is exact; without it, the daemon resolves the
// id only when a single agent owns it (returns 409 Conflict if more than
// one agent matches).
// Plus the completion endpoints (mg-a754):
//
//	POST /scheduler/schedules/{id}/ack  — acknowledge the outstanding fire
//	GET  /scheduler/completion          — delivered:completed roll-up (?agent=X)
func (s *Scheduler) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/scheduler/schedules", s.handleList)
	mux.HandleFunc("/scheduler/schedules/{id}", s.handleByID)
	mux.HandleFunc("/scheduler/schedules/{id}/ack", s.handleAck)
	mux.HandleFunc("/scheduler/completion", s.handleCompletion)
}

// AckRequest is the JSON body for POST /scheduler/schedules/{id}/ack.
type AckRequest struct {
	Agent string `json:"agent,omitempty"`
	Token string `json:"token"`
}

func (s *Scheduler) handleAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	var req AckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	agentName := req.Agent
	if agentName == "" {
		agentName = r.URL.Query().Get("agent")
	}

	res, err := s.Ack(agentName, id, req.Token, s.clock())
	if err != nil {
		switch {
		case errors.Is(err, ErrScheduleNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrNoPendingFire), errors.Is(err, ErrStaleToken):
			// 409, not 400: the request is well-formed, it just lost a race
			// with the next fire. The distinction matters to the caller — a
			// stale ack is worth logging quietly, a malformed one is a bug.
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			if amb, isAmb := err.(*ErrAmbiguousID); isAmb {
				http.Error(w, amb.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (s *Scheduler) handleCompletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	threshold := 0
	if v := r.URL.Query().Get("threshold"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			threshold = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.Completion(r.URL.Query().Get("agent"), threshold))
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
		entry, err := s.addFromRequest(req, s.clock())
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
	agent := r.URL.Query().Get("agent")
	switch r.Method {
	case "GET":
		entry, ok, err := s.lookupByID(agent, id)
		if err != nil {
			if amb, isAmb := err.(*ErrAmbiguousID); isAmb {
				http.Error(w, amb.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, fmt.Sprintf("schedule %q not found", id), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entry)
	case "DELETE":
		removed, err := s.removeByID(agent, id)
		if err != nil {
			if amb, isAmb := err.(*ErrAmbiguousID); isAmb {
				http.Error(w, amb.Error(), http.StatusConflict)
				return
			}
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

// lookupByID resolves a single entry. With agent set, it's an exact (agent,
// id) lookup. Without agent, it falls back to id-only disambiguation —
// returning *ErrAmbiguousID if multiple agents own the id.
func (s *Scheduler) lookupByID(agent, id string) (Entry, bool, error) {
	if agent != "" {
		e, ok := s.Get(agent, id)
		return e, ok, nil
	}
	return s.GetByID(id)
}

// removeByID is the deletion counterpart of lookupByID.
func (s *Scheduler) removeByID(agent, id string) (bool, error) {
	if agent != "" {
		return s.Remove(agent, id)
	}
	return s.RemoveByID(id)
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
