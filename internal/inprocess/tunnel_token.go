// Package inprocess — tunnel_token provides one-time registration tokens for
// the web-gate tunnel system. When a machine starts `orchestra serve --web-gate`,
// it generates a token containing machine metadata. The user copies this token
// to the web app to register the tunnel.
package inprocess

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// TunnelToken contains the machine metadata embedded in a registration token.
// The web app decodes this to verify the tunnel and store connection info.
type TunnelToken struct {
	// Machine identification
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`

	// Network
	LocalIP     string `json:"local_ip"`
	GateAddress string `json:"gate_address"`
	CloudURL    string `json:"cloud_url,omitempty"` // cloud server URL for reverse tunnel

	// Security
	APIKeyHash string `json:"api_key_hash,omitempty"` // SHA-256 of the API key (empty if no auth)
	Nonce      string `json:"nonce"`                  // random nonce for uniqueness

	// Metadata
	ToolCount int    `json:"tool_count"`
	CreatedAt string `json:"created_at"`            // ISO 8601
	Workspace string `json:"workspace,omitempty"`   // absolute workspace path (for multi-workspace tunnels)
}

// TunnelTokenManager handles token generation, storage, and one-time verification.
type TunnelTokenManager struct {
	mu    sync.Mutex
	token *TunnelToken // the current pending token (nil = none or already consumed)
	raw   string       // base64-encoded token string
}

// NewTunnelTokenManager creates a new token manager.
func NewTunnelTokenManager() *TunnelTokenManager {
	return &TunnelTokenManager{}
}

// GenerateToken creates a new registration token with machine metadata.
// Only one token is active at a time — generating a new one invalidates the previous.
func (m *TunnelTokenManager) GenerateToken(gateAddress, apiKey, cloudURL, workspace string, toolCount int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	hostname, _ := os.Hostname()
	localIP := getLocalIP()

	// Generate random nonce (16 bytes → 32 hex chars).
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Normalize wildcard bind addresses to localhost so the token
	// contains a dialable address (net.Listener reports [::]:<port>
	// or 0.0.0.0:<port> for wildcard listeners).
	normalizedAddr := gateAddress
	normalizedAddr = strings.Replace(normalizedAddr, "[::]:", "localhost:", 1)
	normalizedAddr = strings.Replace(normalizedAddr, "0.0.0.0:", "localhost:", 1)

	token := &TunnelToken{
		Hostname:    hostname,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		LocalIP:     localIP,
		GateAddress: normalizedAddr,
		CloudURL:    cloudURL,
		ToolCount:   toolCount,
		Nonce:       hex.EncodeToString(nonce),
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Workspace:   workspace,
	}

	// Hash the API key if provided (never store the key itself in the token).
	if apiKey != "" {
		token.APIKeyHash = hashAPIKey(apiKey)
	}

	// Encode to JSON then base64url (URL-safe, no padding).
	jsonBytes, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("marshal token: %w", err)
	}

	raw := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(jsonBytes)

	m.token = token
	m.raw = raw
	return raw, nil
}

// VerifyToken checks if the provided token matches the current pending token.
// On success, the token is consumed (one-time use) and the decoded token is returned.
// Returns an error if no token is pending, or the token doesn't match.
func (m *TunnelTokenManager) VerifyToken(rawToken string) (*TunnelToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.token == nil {
		return nil, fmt.Errorf("no pending registration token")
	}

	if subtle.ConstantTimeCompare([]byte(rawToken), []byte(m.raw)) != 1 {
		return nil, fmt.Errorf("invalid registration token")
	}

	// Token consumed — one-time use.
	token := m.token
	m.token = nil
	m.raw = ""

	return token, nil
}

// HasPendingToken returns true if there is an unconsumed registration token.
func (m *TunnelTokenManager) HasPendingToken() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token != nil
}

// RevokeToken invalidates any pending token without consuming it.
func (m *TunnelTokenManager) RevokeToken() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = nil
	m.raw = ""
}

// DecodeToken decodes a raw base64url token string into a TunnelToken.
// This is used by the web backend to inspect the token contents.
func DecodeToken(raw string) (*TunnelToken, error) {
	jsonBytes, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}

	var token TunnelToken
	if err := json.Unmarshal(jsonBytes, &token); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}

	return &token, nil
}

// VerifyAPIKeyHash checks if a plaintext API key matches the hash stored in a token.
func VerifyAPIKeyHash(apiKey, hash string) bool {
	return hmac.Equal([]byte(hashAPIKey(apiKey)), []byte(hash))
}

// hashAPIKey returns a SHA-256 hex digest of the API key.
func hashAPIKey(apiKey string) string {
	h := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(h[:])
}

// getLocalIP returns the machine's preferred outbound local IP address.
func getLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		// Fallback: scan interfaces.
		return getLocalIPFallback()
	}
	defer conn.Close()

	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

// getLocalIPFallback scans network interfaces for a non-loopback IPv4 address.
func getLocalIPFallback() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// FormatTokenDisplay returns a terminal-friendly display of the tunnel token
// for the user to copy to the web app. Uses \r\n line endings and plain ASCII
// so it renders correctly whether the terminal is in raw mode or not.
func FormatTokenDisplay(raw string, token *TunnelToken) string {
	auth := "disabled (no API key)"
	if token.APIKeyHash != "" {
		auth = "enabled"
	}

	var b strings.Builder
	b.WriteString("\r\n")
	b.WriteString(fmt.Sprintf("  TUNNEL REGISTRATION TOKEN\r\n"))
	b.WriteString(fmt.Sprintf("  -------------------------\r\n"))
	b.WriteString(fmt.Sprintf("  Machine:   %s\r\n", token.Hostname))
	b.WriteString(fmt.Sprintf("  OS/Arch:   %s/%s\r\n", token.OS, token.Arch))
	b.WriteString(fmt.Sprintf("  Local IP:  %s\r\n", token.LocalIP))
	b.WriteString(fmt.Sprintf("  Gate:      %s\r\n", token.GateAddress))
	b.WriteString(fmt.Sprintf("  Tools:     %d\r\n", token.ToolCount))
	b.WriteString(fmt.Sprintf("  Auth:      %s\r\n", auth))
	if token.Workspace != "" {
		b.WriteString(fmt.Sprintf("  Workspace: %s\r\n", token.Workspace))
	}
	if token.CloudURL != "" {
		b.WriteString(fmt.Sprintf("  Cloud:     %s\r\n", token.CloudURL))
	}
	b.WriteString("\r\n  Copy this token to the web app to register this tunnel:\r\n\r\n")
	b.WriteString(raw)
	b.WriteString("\r\n")
	return b.String()
}
