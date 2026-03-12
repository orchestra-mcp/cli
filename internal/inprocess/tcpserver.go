package inprocess

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/globaldb"
	"github.com/orchestra-mcp/sdk-go/plugin"
)

// TCPServer listens for desktop app connections (Swift, Windows, Linux) and
// proxy connections from other orchestra serve instances. Uses the same
// length-delimited Protobuf protocol as the orchestrator's TCP bridge.
// Each TCP connection is persistent — multiple requests can be sent over a
// single connection (the connection stays open until the client disconnects).
type TCPServer struct {
	addr     string
	router   *Router
	listener net.Listener

	// sessionsMu protects activeSessions.
	sessionsMu sync.Mutex
	// activeSessions tracks session IDs per TCP connection so we can release
	// session locks when the connection closes.
	activeSessions map[net.Conn]string
}

// NewTCPServer creates a TCP server bound to the given address.
func NewTCPServer(addr string, router *Router) *TCPServer {
	return &TCPServer{
		addr:           addr,
		router:         router,
		activeSessions: make(map[net.Conn]string),
	}
}

// Addr returns the actual listening address. Only valid after Listen.
func (s *TCPServer) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// Listen binds the TCP socket without accepting connections. Call Serve to
// start processing. Splitting listen from serve lets the caller detect port
// conflicts and fall back before committing.
func (s *TCPServer) Listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("tcp listen %s: %w", s.addr, err)
	}
	s.listener = ln
	log.Printf("[inprocess] TCP server listening on %s (for desktop apps)", ln.Addr().String())
	return nil
}

// Serve accepts connections on the already-bound listener until the context
// is cancelled. Must call Listen first.
func (s *TCPServer) Serve(ctx context.Context) error {
	if s.listener == nil {
		return fmt.Errorf("tcp server: Listen not called")
	}
	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			return fmt.Errorf("tcp accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

// ListenAndServe binds and serves in one call (convenience wrapper).
func (s *TCPServer) ListenAndServe(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve(ctx)
}

// handleConn reads PluginRequests in a loop until the client disconnects.
// If a StreamStart request arrives, the connection is handed off to
// handleStreamConn. Session IDs are tracked so that session locks can be
// released when the connection closes.
func (s *TCPServer) handleConn(ctx context.Context, conn net.Conn) {
	defer func() {
		conn.Close()
		// Release session locks for this connection.
		s.sessionsMu.Lock()
		sessionID := s.activeSessions[conn]
		delete(s.activeSessions, conn)
		s.sessionsMu.Unlock()
		if sessionID != "" {
			log.Printf("[inprocess] tcp session %s disconnected — releasing locks", sessionID)
			globaldb.ReleaseSessionLocks(sessionID)
		}
	}()

	for {
		var req pluginv1.PluginRequest
		if err := plugin.ReadMessage(conn, &req); err != nil {
			// EOF or read error — client disconnected.
			return
		}

		// Streaming request — takes over the connection.
		if ss := req.GetStreamStart(); ss != nil {
			s.handleStreamConn(ctx, conn, &req, ss)
			return
		}

		// Track session ID from tool calls for disconnect cleanup.
		if tc := req.GetToolCall(); tc != nil && tc.GetSessionId() != "" {
			s.sessionsMu.Lock()
			s.activeSessions[conn] = tc.GetSessionId()
			s.sessionsMu.Unlock()
		}

		resp, err := s.router.Send(ctx, &req)
		if err != nil {
			log.Printf("[inprocess] tcp dispatch: %v", err)
			continue
		}
		resp.RequestId = req.GetRequestId()

		if err := plugin.WriteMessage(conn, resp); err != nil {
			log.Printf("[inprocess] tcp write: %v", err)
			return
		}
	}
}

// handleStreamConn handles a streaming tool call. It looks up the stream
// handler, creates a chunks channel, writes each chunk as a StreamChunk
// Protobuf message, and finishes with StreamEnd.
func (s *TCPServer) handleStreamConn(ctx context.Context, conn net.Conn, req *pluginv1.PluginRequest, ss *pluginv1.StreamStart) {
	toolName := ss.GetToolName()
	streamID := ss.GetStreamId()

	handler, ok := s.router.GetStreamHandler(toolName)
	if !ok {
		// No handler — send StreamEnd with error.
		endResp := &pluginv1.PluginResponse{
			RequestId: req.GetRequestId(),
			Response: &pluginv1.PluginResponse_StreamEnd{
				StreamEnd: &pluginv1.StreamEnd{
					StreamId:     streamID,
					Success:      false,
					ErrorCode:    "tool_not_found",
					ErrorMessage: fmt.Sprintf("no streaming handler for tool %q", toolName),
				},
			},
		}
		_ = plugin.WriteMessage(conn, endResp)
		return
	}

	chunks := make(chan []byte, 64)
	var sequence int64

	// Writer goroutine: drains chunks and writes StreamChunk messages.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for data := range chunks {
			chunkResp := &pluginv1.PluginResponse{
				RequestId: req.GetRequestId(),
				Response: &pluginv1.PluginResponse_StreamChunk{
					StreamChunk: &pluginv1.StreamChunk{
						StreamId:    streamID,
						Data:        data,
						ContentType: "application/json",
						Sequence:    sequence,
					},
				},
			}
			if err := plugin.WriteMessage(conn, chunkResp); err != nil {
				log.Printf("[inprocess] tcp stream write chunk: %v", err)
				return
			}
			sequence++
		}
	}()

	// Call the streaming handler — blocks until done.
	handlerErr := handler(ctx, ss, chunks)
	close(chunks)
	<-writerDone

	// Send StreamEnd.
	endResp := &pluginv1.PluginResponse{
		RequestId: req.GetRequestId(),
		Response: &pluginv1.PluginResponse_StreamEnd{
			StreamEnd: &pluginv1.StreamEnd{
				StreamId:    streamID,
				Success:     handlerErr == nil,
				TotalChunks: sequence,
			},
		},
	}
	if handlerErr != nil {
		endResp.GetStreamEnd().ErrorCode = "stream_error"
		endResp.GetStreamEnd().ErrorMessage = handlerErr.Error()
	}
	if err := plugin.WriteMessage(conn, endResp); err != nil {
		log.Printf("[inprocess] tcp stream write end: %v", err)
	}
}
