package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
)

const (
	reconnectMaxAttempts = 10
	reconnectBaseDelay   = 1 * time.Second
	reconnectMaxDelay    = 10 * time.Second
	dialTimeout          = 5 * time.Second
	healthCheckInterval  = 10 * time.Second
	healthCheckTimeout   = 3 * time.Second
)

// TCPSender implements the Sender interface by forwarding Protobuf requests to
// an existing orchestra instance over a persistent TCP connection. This is used
// by proxy mode: when a new `orchestra serve` is spawned and finds a healthy
// instance, it creates a TCPSender connected to that instance and passes it to
// StdioTransport, bridging stdin/stdout ↔ TCP.
//
// If the connection drops (e.g. orchestrator restart), Send automatically
// reconnects with exponential backoff and retries the request. During reconnect,
// the info file is re-read in case the primary restarted on a different port.
// A background health check detects primary death and exits the proxy
// process so the IDE can restart it as a new primary instance.
type TCPSender struct {
	addr     string
	infoFile string // path to .info file — re-read on reconnect to discover new port
	conn     net.Conn
	mu       sync.Mutex // serializes Send calls (one request at a time)
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewTCPSender connects to an existing orchestra instance's TCP server.
// infoFile is the path to the instance's .info JSON file — it's re-read during
// reconnection in case the primary restarted on a different port.
// It starts a background health check that exits the process if the primary
// instance becomes permanently unreachable.
func NewTCPSender(addr, infoFile string) (*TCPSender, error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &TCPSender{addr: addr, infoFile: infoFile, conn: conn, ctx: ctx, cancel: cancel}
	go s.healthCheck()
	return s, nil
}

// Send forwards a PluginRequest to the remote instance and reads the response.
// It implements the Sender interface used by StdioTransport.
//
// On connection errors (broken pipe, connection reset, EOF), it automatically
// reconnects with exponential backoff and retries the request.
func (s *TCPSender) Send(ctx context.Context, req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp, err := s.sendOnce(req)
	if err == nil {
		return resp, nil
	}

	// Connection is broken — try to reconnect and retry.
	slog.Warn("send failed, attempting reconnect", "error", err, "addr", s.addr)

	if reconnErr := s.reconnect(ctx); reconnErr != nil {
		return nil, fmt.Errorf("send failed (%v) and reconnect failed: %w", err, reconnErr)
	}

	slog.Info("reconnected, retrying request", "addr", s.addr)
	return s.sendOnce(req)
}

// sendOnce performs a single write+read exchange on the current connection.
// It skips unsolicited EventDelivery messages pushed by the TCP server's event
// bus goroutine, which can arrive between request and response and would
// otherwise be misinterpreted as the tool call response.
func (s *TCPSender) sendOnce(req *pluginv1.PluginRequest) (*pluginv1.PluginResponse, error) {
	if err := plugin.WriteMessage(s.conn, req); err != nil {
		return nil, fmt.Errorf("write to remote: %w", err)
	}

	for {
		var resp pluginv1.PluginResponse
		if err := plugin.ReadMessage(s.conn, &resp); err != nil {
			return nil, fmt.Errorf("read from remote: %w", err)
		}
		// Skip unsolicited EventDelivery pushes — the proxy doesn't consume them.
		if resp.GetEventDelivery() != nil {
			continue
		}
		return &resp, nil
	}
}

// refreshAddr re-reads the .info file to discover the current TCP address.
// If the primary restarted on a different port, we pick up the new address.
func (s *TCPSender) refreshAddr() {
	if s.infoFile == "" {
		return
	}
	data, err := os.ReadFile(s.infoFile)
	if err != nil {
		return
	}
	var info struct {
		TCPAddr string `json:"tcp_addr"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return
	}
	if info.TCPAddr != "" && info.TCPAddr != s.addr {
		slog.Info("primary address changed", "old", s.addr, "new", info.TCPAddr)
		s.addr = info.TCPAddr
	}
}

// reconnect closes the dead connection and dials a new one, retrying up to
// reconnectMaxAttempts times with exponential backoff (1s, 2s, 4s, ... capped
// at 10s). Re-reads the .info file on each attempt in case the primary
// restarted on a different port. Respects context cancellation.
func (s *TCPSender) reconnect(ctx context.Context) error {
	s.conn.Close()

	delay := reconnectBaseDelay
	var lastErr error
	for i := 0; i < reconnectMaxAttempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("reconnect cancelled: %w", ctx.Err())
			case <-s.ctx.Done():
				return fmt.Errorf("proxy shutting down")
			case <-time.After(delay):
			}
			// Exponential backoff, capped.
			delay = delay * 2
			if delay > reconnectMaxDelay {
				delay = reconnectMaxDelay
			}
		}

		// Re-read .info file — primary may have restarted on a different port.
		s.refreshAddr()

		conn, err := net.DialTimeout("tcp", s.addr, dialTimeout)
		if err != nil {
			lastErr = err
			slog.Warn("reconnect attempt failed", "attempt", i+1, "max", reconnectMaxAttempts, "addr", s.addr, "error", err)
			continue
		}
		s.conn = conn
		slog.Info("reconnected", "addr", s.addr, "attempt", i+1, "max", reconnectMaxAttempts)
		return nil
	}
	return fmt.Errorf("reconnect to %s after %d attempts: %w", s.addr, reconnectMaxAttempts, lastErr)
}

// healthCheck periodically probes the primary instance. If the instance is
// unreachable for multiple consecutive checks, the proxy exits so the IDE
// can restart orchestra serve as a new primary.
func (s *TCPSender) healthCheck() {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	consecutiveFails := 0
	const maxConsecutiveFails = 3

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			// Re-read .info file in case primary restarted on a different port.
			s.refreshAddr()

			conn, err := net.DialTimeout("tcp", s.addr, healthCheckTimeout)
			if err != nil {
				consecutiveFails++
				slog.Warn("health check failed", "consecutive", consecutiveFails, "max", maxConsecutiveFails, "error", err)
				if consecutiveFails >= maxConsecutiveFails {
					slog.Error("primary instance unreachable, exiting proxy", "addr", s.addr, "checks", maxConsecutiveFails)
					fmt.Fprintf(os.Stderr, "orchestra: primary instance died — restart to become new primary\n")
					s.cancel()
					os.Exit(1)
				}
			} else {
				conn.Close()
				consecutiveFails = 0
			}
		}
	}
}

// Close closes the TCP connection and stops the health check.
func (s *TCPSender) Close() error {
	s.cancel()
	return s.conn.Close()
}
