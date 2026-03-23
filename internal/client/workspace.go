package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/drellem2/pogo/internal/workspace"
)

// QueryWorkspaceSymbols searches for symbols across all workspace repos.
func QueryWorkspaceSymbols(query string, repo string, limit int) (*workspace.SymbolResponse, error) {
	u := serverURL + "/workspace/symbols?query=" + url.QueryEscape(query)
	if repo != "" {
		u += "&repo=" + url.QueryEscape(repo)
	}
	if limit > 0 {
		u += fmt.Sprintf("&limit=%d", limit)
	}
	r, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("server returned %d: %s", r.StatusCode, string(body))
	}
	var resp workspace.SymbolResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// QueryWorkspaceSymbolsPost searches for symbols using a POST request body.
func QueryWorkspaceSymbolsPost(q workspace.SymbolQuery) (*workspace.SymbolResponse, error) {
	body, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	r, err := http.Post(serverURL+"/workspace/symbols", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("server returned %d: %s", r.StatusCode, string(respBody))
	}
	var resp workspace.SymbolResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
