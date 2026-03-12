package inprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	// claimPollInterval is how often the CLI polls for claim results.
	claimPollInterval = 2 * time.Second

	// claimTimeout is how long to wait before giving up on the claim.
	claimTimeout = 5 * time.Minute
)

// claimResponse is the JSON response from POST /api/tunnels/claim.
type claimResponse struct {
	TunnelID        string `json:"tunnel_id"`
	ConnectionToken string `json:"connection_token"`
	TeamID          string `json:"team_id"`
	AuthToken       string `json:"auth_token"`
	Workspace       string `json:"workspace"`
}

// ClaimTunnel polls the cloud server's claim endpoint until the user registers
// the tunnel in the web app. Once registered, it returns the tunnel ID and
// connection token needed to establish the reverse tunnel.
//
// The nonce is the secret from the registration token that the CLI generated.
// The cloud server stores a nonce→credentials mapping when the user pastes the
// token in the web app.
func ClaimTunnel(ctx context.Context, cloudURL, nonce string) (tunnelID, connectionToken, teamID, authToken string, err error) {
	claimURL := fmt.Sprintf("%s/api/tunnels/claim", cloudURL)
	body, _ := json.Marshal(map[string]string{"nonce": nonce})

	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(claimTimeout)

	for {
		select {
		case <-ctx.Done():
			return "", "", "", "", ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return "", "", "", "", fmt.Errorf("claim timed out after %s — token was not registered in the web app", claimTimeout)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, claimURL, bytes.NewReader(body))
		if err != nil {
			return "", "", "", "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			// Network error — retry.
			log.Printf("[claim] poll error: %v (retrying)", err)
			time.Sleep(claimPollInterval)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			var result claimResponse
			if err := json.Unmarshal(respBody, &result); err != nil {
				return "", "", "", "", fmt.Errorf("parse claim response: %w", err)
			}
			return result.TunnelID, result.ConnectionToken, result.TeamID, result.AuthToken, nil

		case http.StatusNotFound:
			// Not registered yet — keep polling.
			time.Sleep(claimPollInterval)
			continue

		case http.StatusGone:
			return "", "", "", "", fmt.Errorf("claim expired — generate a new token")

		default:
			return "", "", "", "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
		}
	}
}
