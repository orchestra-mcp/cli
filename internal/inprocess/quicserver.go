package inprocess

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	sdkplugin "github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/quic-go/quic-go"
)

// QUICBridge is a QUIC listener that child plugin processes connect to for
// storage access and cross-plugin tool calls. The router proxies all requests
// through its in-process handlers.
type QUICBridge struct {
	router   *Router
	listener *quic.Listener
	addr     string
}

// ListenAndServeQUIC starts a QUIC listener using mTLS certificates from the
// given certsDir. Child plugins connect to this address (passed as
// --orchestrator-addr) to make storage and cross-plugin RPC calls.
//
// Returns the actual bound address (e.g. "127.0.0.1:56789") which should be
// passed to spawnPlugin when starting child processes.
func (r *Router) ListenAndServeQUIC(ctx context.Context, addr string, certsDir string) (string, error) {
	serverTLS, err := sdkplugin.ServerTLSConfig(certsDir, "orchestra-host")
	if err != nil {
		return "", fmt.Errorf("server TLS: %w", err)
	}

	// quic-go requires NextProtos to be set for TLS handshake.
	serverTLS.NextProtos = []string{"orchestra-plugin"}

	listener, err := quic.ListenAddr(addr, serverTLS, &quic.Config{
		MaxIdleTimeout:  5 * time.Minute,
		KeepAlivePeriod: 15 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("quic listen %s: %w", addr, err)
	}

	actualAddr := listener.Addr().String()

	bridge := &QUICBridge{
		router:   r,
		listener: listener,
		addr:     actualAddr,
	}

	go bridge.serve(ctx)

	log.Printf("[inprocess] QUIC bridge listening on %s (for child plugin storage/cross-plugin calls)", actualAddr)
	return actualAddr, nil
}

// serve accepts QUIC connections and handles them in goroutines.
func (b *QUICBridge) serve(ctx context.Context) {
	defer b.listener.Close()

	go func() {
		<-ctx.Done()
		b.listener.Close()
	}()

	for {
		conn, err := b.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // graceful shutdown
			}
			log.Printf("[inprocess] quic accept: %v", err)
			return
		}
		go b.handleConn(ctx, conn)
	}
}

// handleConn accepts bidirectional QUIC streams from a child plugin. Each
// stream carries one request-response pair using the SDK's length-delimited
// Protobuf framing.
func (b *QUICBridge) handleConn(ctx context.Context, conn quic.Connection) {
	defer conn.CloseWithError(0, "")

	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return // connection closed
		}
		go b.handleStream(ctx, stream)
	}
}

// handleStream reads a PluginRequest, dispatches via the router, and writes
// the PluginResponse. Same framing as TCPServer.handleConn.
func (b *QUICBridge) handleStream(ctx context.Context, stream quic.Stream) {
	defer stream.Close()

	var req pluginv1.PluginRequest
	if err := sdkplugin.ReadMessage(stream, &req); err != nil {
		return // stream closed or malformed request
	}

	resp, err := b.router.Send(ctx, &req)
	if err != nil {
		log.Printf("[inprocess] quic bridge dispatch: %v", err)
		return
	}
	resp.RequestId = req.GetRequestId()

	if err := sdkplugin.WriteMessage(stream, resp); err != nil {
		log.Printf("[inprocess] quic bridge write: %v", err)
	}
}

// ClientTLSConfigForBridge returns a TLS config suitable for the orchestra host
// to connect to child plugin QUIC servers. This is a convenience wrapper around
// the SDK's ClientTLSConfig with the correct ALPN protocol.
func ClientTLSConfigForBridge(certsDir string) (*tls.Config, error) {
	cfg, err := sdkplugin.ClientTLSConfig(certsDir, "orchestra-host-client")
	if err != nil {
		return nil, err
	}
	cfg.NextProtos = []string{"orchestra-plugin"}
	return cfg, nil
}
