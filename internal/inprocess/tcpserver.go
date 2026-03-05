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

// handleConn reads a single PluginRequest. If it's a StreamStart, the
// connection is handed off to handleStreamConn for persistent streaming.
// Otherwise it dispatches a single request/response pair via the router.
func (s *TCPServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	var req pluginv1.PluginRequest
	if err := plugin.ReadMessage(conn, &req); err != nil {
		log.Printf("[inprocess] tcp read: %v", err)
		return
	}

	// Streaming request — keep connection alive for multiple chunks.
	if ss := req.GetStreamStart(); ss != nil {
		s.handleStreamConn(ctx, conn, &req, ss)
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
