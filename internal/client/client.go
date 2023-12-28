////////////////////////////////////////////////////////////////////////////////
////////// Http client for pogod ///////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/marginalia-gaming/pogo/internal/project"
)

type NoResp struct{}

type ClientResp interface {
	[]project.Project | []NoResp
}

func HealthCheck() error {
	_, err := http.Post("http://localhost:10000/health", "application/json",
		nil)
	return err
}

func StartServer() error {
	// Store the result of os.exec("pogod") in a variable and describe its type
	// If the type is a pointer to a process, then the server is running
	cmd := exec.Command("pogod")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// Run closure with health check
func RunWithHealthCheck[T ClientResp](run func() (T, error)) (T, error) {
	err := HealthCheck()
	if err != nil {
		err = StartServer()
		if err != nil {
			return nil, err
		}
		success := false
		// Loop for up to half a second to check if the server is running
		// Get current time
		startTime := time.Now()
		// Inside for loop, check current time against startTime
		for time.Now().Sub(startTime) < 500*time.Millisecond {
			_, err = http.Post("http://localhost:8080/health",
				"application/json", nil)
			if err == nil {
				success = true
				break
			}
		}
		if !success {
			return nil, err
		}		
	}
	return run()
}

func GetProjects() ([]project.Project, error) {
	projs, err := RunWithHealthCheck(func() ([]project.Project, error) {
		r, err := http.Get("http://localhost:10000/projects")
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		// Deserialize projResp
		// Do json demarshal from http response
		var projs []project.Project
		err = json.Unmarshal(body, &projs)
		if err != nil {
			return nil, err
		}
		return projs, nil
	})
	if err != nil {
		return nil, err
	}
	return projs, nil
}

func Visit(path string) error {
	_, err := RunWithHealthCheck(func() ([]NoResp, error) {
		r, err := http.Post("http://localhost:10000/file",
			"application/json",
			strings.NewReader(
				fmt.Sprintf(`{"path": "%s"}`, path)))
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		return nil, nil
	})
	if err != nil {
		return err
	}
	return nil
}
