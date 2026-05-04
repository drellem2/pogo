package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
