package inprocess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- TunnelTokenManager ---

func TestTunnelTokenGenerate(t *testing.T) {
	m := NewTunnelTokenManager()
	raw, err := m.GenerateToken("localhost:9201", "my-key", "", "", 42)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if raw == "" {
		t.Fatal("expected non-empty token")
	}
	if !m.HasPendingToken() {
		t.Error("expected pending token after generation")
	}
}

func TestTunnelTokenVerifySuccess(t *testing.T) {
	m := NewTunnelTokenManager()
	raw, err := m.GenerateToken("localhost:9201", "my-key", "", "", 42)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	token, err := m.VerifyToken(raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if token.GateAddress != "localhost:9201" {
		t.Errorf("gate_address: got %q", token.GateAddress)
	}
	if token.ToolCount != 42 {
		t.Errorf("tool_count: got %d", token.ToolCount)
	}
	if token.APIKeyHash == "" {
		t.Error("expected non-empty api_key_hash")
	}
	if token.Hostname == "" {
		t.Error("expected non-empty hostname")
	}
	if token.OS == "" {
		t.Error("expected non-empty os")
	}
	if token.Nonce == "" {
		t.Error("expected non-empty nonce")
	}
	if token.CreatedAt == "" {
		t.Error("expected non-empty created_at")
	}
}

func TestTunnelTokenOneTimeUse(t *testing.T) {
	m := NewTunnelTokenManager()
	raw, _ := m.GenerateToken("localhost:9201", "", "", "", 10)

	// First verify succeeds.
	if _, err := m.VerifyToken(raw); err != nil {
		t.Fatalf("first verify: %v", err)
	}

	// Second verify fails (consumed).
	if _, err := m.VerifyToken(raw); err == nil {
		t.Fatal("expected error on second verify (token consumed)")
	}

	if m.HasPendingToken() {
		t.Error("expected no pending token after consumption")
	}
}

func TestTunnelTokenInvalid(t *testing.T) {
	m := NewTunnelTokenManager()
	m.GenerateToken("localhost:9201", "", "", "", 10)

	// Wrong token.
	if _, err := m.VerifyToken("wrong-token"); err == nil {
		t.Fatal("expected error for invalid token")
	}

	// Token should still be pending after failed attempt.
	if !m.HasPendingToken() {
		t.Error("expected pending token after failed verify")
	}
}

func TestTunnelTokenNoPending(t *testing.T) {
	m := NewTunnelTokenManager()
	if _, err := m.VerifyToken("any-token"); err == nil {
		t.Fatal("expected error when no pending token")
	}
}

func TestTunnelTokenRevoke(t *testing.T) {
	m := NewTunnelTokenManager()
	raw, _ := m.GenerateToken("localhost:9201", "", "", "", 10)

	m.RevokeToken()

	if m.HasPendingToken() {
		t.Error("expected no pending token after revoke")
	}
	if _, err := m.VerifyToken(raw); err == nil {
		t.Fatal("expected error after revoke")
	}
}

func TestTunnelTokenRegenerate(t *testing.T) {
	m := NewTunnelTokenManager()
	raw1, _ := m.GenerateToken("localhost:9201", "", "", "", 10)
	raw2, _ := m.GenerateToken("localhost:9202", "", "", "", 20)

	if raw1 == raw2 {
		t.Fatal("expected different tokens on regenerate")
	}

	// Old token should fail.
	if _, err := m.VerifyToken(raw1); err == nil {
		t.Fatal("expected old token to fail after regenerate")
	}

	// New token should work.
	token, err := m.VerifyToken(raw2)
	if err != nil {
		t.Fatalf("verify new: %v", err)
	}
	if token.GateAddress != "localhost:9202" {
		t.Errorf("gate_address: got %q", token.GateAddress)
	}
}

func TestTunnelTokenNoAuth(t *testing.T) {
	m := NewTunnelTokenManager()
	raw, _ := m.GenerateToken("localhost:9201", "", "", "", 10)

	token, _ := m.VerifyToken(raw)
	if token.APIKeyHash != "" {
		t.Errorf("expected empty api_key_hash when no auth, got %q", token.APIKeyHash)
	}
}

// --- DecodeToken ---

func TestDecodeToken(t *testing.T) {
	m := NewTunnelTokenManager()
	raw, _ := m.GenerateToken("localhost:9201", "key123", "", "", 5)

	token, err := DecodeToken(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if token.GateAddress != "localhost:9201" {
		t.Errorf("gate_address: got %q", token.GateAddress)
	}
	if token.ToolCount != 5 {
		t.Errorf("tool_count: got %d", token.ToolCount)
	}
}

func TestDecodeTokenInvalid(t *testing.T) {
	if _, err := DecodeToken("not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

// --- VerifyAPIKeyHash ---

func TestVerifyAPIKeyHash(t *testing.T) {
	hash := hashAPIKey("my-secret")
	if !VerifyAPIKeyHash("my-secret", hash) {
		t.Error("expected hash to match")
	}
	if VerifyAPIKeyHash("wrong-secret", hash) {
		t.Error("expected hash to not match")
	}
}

// --- FormatTokenDisplay ---

func TestFormatTokenDisplay(t *testing.T) {
	token := &TunnelToken{
		Hostname:    "test-host",
		OS:          "darwin",
		Arch:        "arm64",
		LocalIP:     "192.168.1.100",
		GateAddress: "localhost:9201",
		ToolCount:   42,
		APIKeyHash:  "abc123",
		CreatedAt:   "2026-03-07T12:00:00Z",
	}

	display := FormatTokenDisplay("dGVzdC10b2tlbg", token)
	if !strings.Contains(display, "test-host") {
		t.Error("display should contain hostname")
	}
	if !strings.Contains(display, "darwin/arm64") {
		t.Error("display should contain OS/Arch")
	}
	if !strings.Contains(display, "TUNNEL REGISTRATION TOKEN") {
		t.Error("display should contain title")
	}
	if !strings.Contains(display, "enabled") {
		t.Error("display should show auth enabled")
	}
}

func TestFormatTokenDisplayNoAuth(t *testing.T) {
	token := &TunnelToken{
		Hostname:    "test-host",
		OS:          "linux",
		Arch:        "amd64",
		LocalIP:     "10.0.0.5",
		GateAddress: ":9201",
		ToolCount:   10,
		CreatedAt:   "2026-03-07T12:00:00Z",
	}

	display := FormatTokenDisplay("c2hvcnQ", token)
	if !strings.Contains(display, "disabled") {
		t.Error("display should show auth disabled")
	}
}

// --- /register endpoint ---

func registerTestServer(t *testing.T, apiKey string) (*httptest.Server, *WebGateServer) {
	t.Helper()
	router := testRouter()
	wg := NewWebGateServer(router, apiKey, "", "", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", wg.handleUpgrade)
	mux.HandleFunc("GET /health", wg.handleHealth)
	mux.HandleFunc("POST /register", wg.handleRegister)

	ts := httptest.NewServer(wg.corsMiddleware(mux))
	t.Cleanup(ts.Close)
	return ts, wg
}

func TestRegisterEndpointSuccess(t *testing.T) {
	ts, wg := registerTestServer(t, "my-key")

	// Generate a token.
	raw, _, err := wg.GenerateRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// POST /register with the token.
	body := strings.NewReader(`{"token":"` + raw + `"}`)
	resp, err := http.Post(ts.URL+"/register", "application/json", body)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if result["verified"] != true {
		t.Errorf("verified: got %v", result["verified"])
	}
	tunnel, ok := result["tunnel"].(map[string]any)
	if !ok {
		t.Fatalf("expected tunnel object, got %T", result["tunnel"])
	}
	if tunnel["has_auth"] != true {
		t.Errorf("has_auth: got %v", tunnel["has_auth"])
	}
}

func TestRegisterEndpointInvalidToken(t *testing.T) {
	ts, wg := registerTestServer(t, "")

	// Generate a token but send a different one.
	wg.GenerateRegistrationToken()

	body := strings.NewReader(`{"token":"wrong-token"}`)
	resp, err := http.Post(ts.URL+"/register", "application/json", body)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRegisterEndpointMissingToken(t *testing.T) {
	ts, _ := registerTestServer(t, "")

	body := strings.NewReader(`{"token":""}`)
	resp, err := http.Post(ts.URL+"/register", "application/json", body)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestRegisterEndpointNoPending(t *testing.T) {
	ts, _ := registerTestServer(t, "")

	// No token generated — should fail.
	body := strings.NewReader(`{"token":"some-token"}`)
	resp, err := http.Post(ts.URL+"/register", "application/json", body)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRegisterEndpointConsumed(t *testing.T) {
	ts, wg := registerTestServer(t, "")

	raw, _, _ := wg.GenerateRegistrationToken()

	// First registration succeeds.
	body := strings.NewReader(`{"token":"` + raw + `"}`)
	resp, _ := http.Post(ts.URL+"/register", "application/json", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first register: got %d", resp.StatusCode)
	}

	// Second registration fails (consumed).
	body = strings.NewReader(`{"token":"` + raw + `"}`)
	resp, _ = http.Post(ts.URL+"/register", "application/json", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("second register: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRegisterEndpointInvalidJSON(t *testing.T) {
	ts, _ := registerTestServer(t, "")

	body := strings.NewReader(`{invalid}`)
	resp, err := http.Post(ts.URL+"/register", "application/json", body)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// --- GenerateRegistrationToken ---

func TestGenerateRegistrationToken(t *testing.T) {
	router := testRouter()
	wg := NewWebGateServer(router, "test-key", "", "", nil)

	raw, token, err := wg.GenerateRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if raw == "" {
		t.Fatal("expected non-empty raw token")
	}
	if token == nil {
		t.Fatal("expected non-nil token")
	}
	if token.ToolCount < 2 {
		t.Errorf("tool_count: got %d, want >= 2", token.ToolCount)
	}
	if token.APIKeyHash == "" {
		t.Error("expected non-empty api_key_hash")
	}
}
