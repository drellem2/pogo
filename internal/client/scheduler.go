package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/drellem2/pogo/internal/scheduler"
)

// AddSchedule registers a schedule with pogod and returns the canonical
// stored entry (with daemon-side ID + computed NextFire filled in).
func AddSchedule(req scheduler.AddRequest) (*scheduler.Entry, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	r, err := http.Post(serverURL+"/scheduler/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("schedule add failed (%d): %s", r.StatusCode, string(msg))
	}
	var entry scheduler.Entry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListSchedules returns all schedules, optionally filtered by agent.
func ListSchedules(agent string) ([]scheduler.Entry, error) {
	u := serverURL + "/scheduler/schedules"
	if agent != "" {
		u += "?agent=" + url.QueryEscape(agent)
	}
	r, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("schedule list failed (%d): %s", r.StatusCode, string(msg))
	}
	var entries []scheduler.Entry
	if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// AckSchedule acknowledges the outstanding fire for a schedule, recording that
// the work the fire triggered actually finished (mg-a754). agent may be empty
// when a single agent owns the id.
//
// A stale or already-redeemed token comes back as a conflict error rather than
// being silently accepted — see scheduler.ErrStaleToken.
func AckSchedule(agent, id, token string) (*scheduler.AckResult, error) {
	body, err := json.Marshal(scheduler.AckRequest{Agent: agent, Token: token})
	if err != nil {
		return nil, err
	}
	u := serverURL + "/scheduler/schedules/" + url.PathEscape(id) + "/ack"
	r, err := http.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("schedule %q not found", id)
	}
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(msg)))
	}
	var res scheduler.AckResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SchedulerCompletion fetches the delivered:completed roll-up, optionally
// filtered by agent. threshold <= 0 uses the daemon's default.
func SchedulerCompletion(agent string, threshold int) (*scheduler.CompletionStats, error) {
	u := serverURL + "/scheduler/completion"
	q := url.Values{}
	if agent != "" {
		q.Set("agent", agent)
	}
	if threshold > 0 {
		q.Set("threshold", strconv.Itoa(threshold))
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("completion query failed (%d): %s", r.StatusCode, string(msg))
	}
	var stats scheduler.CompletionStats
	if err := json.NewDecoder(r.Body).Decode(&stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// RemoveSchedule deletes a schedule. agent may be empty, in which case the
// daemon disambiguates by id — when multiple agents own a schedule with that
// id, the request fails with a conflict error and the caller must pass the
// owning agent explicitly.
func RemoveSchedule(agent, id string) error {
	u := serverURL + "/scheduler/schedules/" + url.PathEscape(id)
	if agent != "" {
		u += "?agent=" + url.QueryEscape(agent)
	}
	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return fmt.Errorf("schedule %q not found", id)
	}
	if r.StatusCode == http.StatusConflict {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("%s", strings.TrimSpace(string(msg)))
	}
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("schedule delete failed (%d): %s", r.StatusCode, string(msg))
	}
	return nil
}
