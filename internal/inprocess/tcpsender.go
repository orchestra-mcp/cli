package inprocess

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
)

const (
	reconnectAttempts = 3
	reconnectDelay    = 1 * time.Second
	dialTimeout       = 5 * time.Second
)

// TCPSender implements the Sender interface by forwarding Protobuf requests to
// an existing orchestra instance over a persistent TCP connection. This is used
// by proxy mode: when a new `orchestra serve` is spawned and finds a healthy
// instance, it creates a TCPSender connected to that instance and passes it to
// StdioTransport, bridging stdin/stdout ↔ TCP.
//
// If the connection drops (e.g. orchestrator restart), Send automatically
// reconnects and retries the request.
type TCPSender struct {
	addr string
	conn net.Conn
	mu   sync.Mutex // serializes Send calls (one request at a time)
}

// NewTCPSender connects to an existing orchestra instance's TCP server.
func NewTCPSender(addr string) (*TCPSender, error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	return &TCPSender{addr: addr, conn: conn}, nil
}

// Send forwards a PluginRequest to the remote instance and reads the response.
// It implements the Sender interface used by StdioTransport.
//
// On connection errors (broken pipe, connection reset, EOF), it automatically
// reconnects and retries the request once.
func (s *TCPSender) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp, err := s.sendOnce(req)
	if err == nil {
		return resp, nil
	}

	// Connection is broken — try to reconnect and retry.
	log.Printf("[proxy] send failed (%v), attempting reconnect to %s", err, s.addr)

	if reconnErr := s.reconnect(); reconnErr != nil {
		return nil, fmt.Errorf("send failed (%v) and reconnect failed: %w", err, reconnErr)
	}

	log.Printf("[proxy] reconnected to %s, retrying request", s.addr)
	return s.sendOnce(req)
}

// sendOnce performs a single write+read exchange on the current connection.
func (s *TCPSender) sendOnce(req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	if err := plugin.WriteMessage(s.conn, req); err != nil {
		return nil, fmt.Errorf("write to remote: %w", err)
	}

	var resp pluginv1.PluginResponse
	if err := plugin.ReadMessage(s.conn, &resp); err != nil {
		return nil, fmt.Errorf("read from remote: %w", err)
	}

	return &resp, nil
}

// reconnect closes the dead connection and dials a new one, retrying up to
// reconnectAttempts times with reconnectDelay between attempts.
func (s *TCPSender) reconnect() error {
	s.conn.Close()

	var lastErr error
	for i := 0; i < reconnectAttempts; i++ {
		if i > 0 {
			time.Sleep(reconnectDelay)
		}
		conn, err := net.DialTimeout("tcp", s.addr, dialTimeout)
		if err != nil {
			lastErr = err
			log.Printf("[proxy] reconnect attempt %d/%d failed: %v", i+1, reconnectAttempts, err)
			continue
		}
		s.conn = conn
		return nil
	}
	return fmt.Errorf("reconnect to %s after %d attempts: %w", s.addr, reconnectAttempts, lastErr)
}

// Close closes the TCP connection.
func (s *TCPSender) Close() error {
	return s.conn.Close()
}
