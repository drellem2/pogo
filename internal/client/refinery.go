package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/drellem2/pogo/internal/refinery"
)

// SubmitMerge submits a branch to the refinery merge queue.
func SubmitMerge(req refinery.SubmitRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	r, err := http.Post(serverURL+"/refinery/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(r.Body)
		return "", fmt.Errorf("submit failed: %s", string(msg))
	}
	var result map[string]string
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["id"], nil
}
