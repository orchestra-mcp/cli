package inprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"
)

// AutoRegisterRequest is the JSON body sent to POST /api/tunnels/auto-register.
type AutoRegisterRequest struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	LocalIP      string `json:"local_ip"`
	GateAddress  string `json:"gate_address"`
	Workspace    string `json:"workspace"`
	ToolCount    int    `json:"tool_count"`
	Version      string `json:"version"`
}

// AutoRegisterResponse is the JSON response from POST /api/tunnels/auto-register.
type AutoRegisterResponse struct {
	TunnelID        string `json:"tunnel_id"`
	ConnectionToken string `json:"connection_token"`
	TeamID          string `json:"team_id"`
	AuthToken       string `json:"auth_token"`
	Workspace       string `json:"workspace"`
	Reconnected     bool   `json:"reconnected"`
}

// AutoRegisterTunnel calls POST /api/tunnels/auto-register on the cloud server
// using the user's stored JWT. Returns the tunnel credentials immediately.
func AutoRegisterTunnel(cloudURL, authToken, gateAddress, workspace string, toolCount int) (*AutoRegisterResponse, error) {
	hostname, _ := os.Hostname()

	body := AutoRegisterRequest{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		LocalIP:      localIP(),
		GateAddress:  gateAddress,
		Workspace:    workspace,
		ToolCount:    toolCount,
		Version:      "1.0.0",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := cloudURL + "/api/tunnels/auto-register"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auto-register request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("auto-register failed (status %d): %s", resp.StatusCode, respBody)
	}

	var result AutoRegisterResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// localIP returns the preferred outbound local IP address.
func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
