package inprocess

import (
	"context"
	"fmt"
	"log"
	"net"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
)

// TCPServer listens for desktop app connections (Swift, Windows, Linux) using
// the same length-delimited Protobuf protocol as the orchestrator's TCP bridge.
// Each TCP connection handles one request/response pair.
type TCPServer struct {
	addr     string
	router   *Router
	listener net.Listener
}

// NewTCPServer creates a TCP server bound to the given address.
func NewTCPServer(addr string, router *Router) *TCPServer {
	return &TCPServer{
		addr:   addr,
		router: router,
	}
}

// Addr returns the actual listening address. Only valid after ListenAndServe.
func (s *TCPServer) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// ListenAndServe starts the TCP listener and processes connections until the
// context is cancelled. Each connection receives one PluginRequest and gets
// one PluginResponse back, using the SDK's length-delimited Protobuf framing.
func (s *TCPServer) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("tcp listen %s: %w", s.addr, err)
	}
	s.listener = ln
	log.Printf("[inprocess] TCP server listening on %s (for desktop apps)", ln.Addr().String())

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			return fmt.Errorf("tcp accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

// handleConn reads a single PluginRequest, dispatches via the router, and
// writes the PluginResponse. Uses the SDK's ReadMessage/WriteMessage which
// implement the 4-byte length-delimited Protobuf framing.
func (s *TCPServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	var req pluginv1.PluginRequest
	if err := plugin.ReadMessage(conn, &req); err != nil {
		log.Printf("[inprocess] tcp read: %v", err)
		return
	}

	resp, err := s.router.Send(ctx, &req)
	if err != nil {
		log.Printf("[inprocess] tcp dispatch: %v", err)
		return
	}
	resp.RequestId = req.GetRequestId()

	if err := plugin.WriteMessage(conn, resp); err != nil {
		log.Printf("[inprocess] tcp write: %v", err)
	}
}
