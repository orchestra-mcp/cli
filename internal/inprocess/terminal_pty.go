package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/creack/pty"
	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/helpers"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxOutputBuffer = 64 * 1024 // 64KB

// TerminalManager manages PTY terminal sessions for remote access via MCP tools.
// Sessions can be created directly (Go PTY) or relayed from the desktop Flutter app.
type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*terminalSession
	counter  int
}

type terminalSession struct {
	id      string
	cmd     *exec.Cmd
	ptmx    *os.File
	buf     []byte
	bufMu   sync.Mutex
	created time.Time
	shell   string
}

// NewTerminalManager creates a new TerminalManager.
func NewTerminalManager() *TerminalManager {
	return &TerminalManager{
		sessions: make(map[string]*terminalSession),
	}
}

// RegisterTools registers all 6 terminal PTY tools with the plugin builder.
func (tm *TerminalManager) RegisterTools(builder *plugin.PluginBuilder) {
	builder.RegisterTool("create_terminal",
		"Create a new terminal session (spawns a shell process with PTY)",
		createTerminalSchema(), tm.handleCreateTerminal())

	builder.RegisterTool("send_input",
		"Send input data to a terminal session",
		sendInputSchema(), tm.handleSendInput())

	builder.RegisterTool("terminal_output",
		"Read new output from a terminal session (drains the buffer)",
		terminalOutputSchema(), tm.handleTerminalOutput())

	builder.RegisterTool("resize_terminal",
		"Resize a terminal session",
		resizeTerminalSchema(), tm.handleResizeTerminal())

	builder.RegisterTool("close_terminal",
		"Close a terminal session and kill the process",
		closeTerminalSchema(), tm.handleCloseTerminal())

	builder.RegisterTool("list_terminals",
		"List all active terminal sessions",
		listTerminalsSchema(), tm.handleListTerminals())
}

// readLoop continuously reads from PTY and appends to buffer.
func (s *terminalSession) readLoop() {
	tmp := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(tmp)
		if n > 0 {
			s.bufMu.Lock()
			s.buf = append(s.buf, tmp[:n]...)
			if len(s.buf) > maxOutputBuffer {
				s.buf = s.buf[len(s.buf)-maxOutputBuffer:]
			}
			s.bufMu.Unlock()
		}
		if err != nil {
			break
		}
	}
}

// drainOutput returns and clears the buffered output.
func (s *terminalSession) drainOutput() string {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	out := string(s.buf)
	s.buf = s.buf[:0]
	return out
}

// --- Schemas ---

func createTerminalSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"shell": map[string]any{
				"type":        "string",
				"description": "Shell to launch (defaults to $SHELL or /bin/sh)",
			},
		},
	})
	return s
}

func sendInputSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"terminal_id": map[string]any{"type": "string", "description": "Terminal session ID"},
			"data":        map[string]any{"type": "string", "description": "Input data to send"},
		},
		"required": []any{"terminal_id", "data"},
	})
	return s
}

func terminalOutputSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"terminal_id": map[string]any{"type": "string", "description": "Terminal session ID"},
		},
		"required": []any{"terminal_id"},
	})
	return s
}

func resizeTerminalSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"terminal_id": map[string]any{"type": "string", "description": "Terminal session ID"},
			"cols":        map[string]any{"type": "number", "description": "Width in columns"},
			"rows":        map[string]any{"type": "number", "description": "Height in rows"},
		},
		"required": []any{"terminal_id", "cols", "rows"},
	})
	return s
}

func closeTerminalSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"terminal_id": map[string]any{"type": "string", "description": "Terminal session ID to close"},
		},
		"required": []any{"terminal_id"},
	})
	return s
}

func listTerminalsSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{"type": "object", "properties": map[string]any{}})
	return s
}

// --- Handlers ---

func (tm *TerminalManager) handleCreateTerminal() func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		shell := helpers.GetString(req.Arguments, "shell")
		if shell == "" {
			if runtime.GOOS == "windows" {
				shell = "cmd.exe"
			} else {
				shell = os.Getenv("SHELL")
				if shell == "" {
					shell = "/bin/sh"
				}
			}
		}

		tm.counter++
		id := fmt.Sprintf("term-%d", tm.counter)

		cmd := exec.Command(shell)
		cmd.Env = append(os.Environ(), "TERM=xterm-256color")

		ptmx, err := pty.Start(cmd)
		if err != nil {
			return helpers.ErrorResult("pty_error", fmt.Sprintf("failed to start PTY: %v", err)), nil
		}

		// Set initial size
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 25})

		sess := &terminalSession{
			id:      id,
			cmd:     cmd,
			ptmx:    ptmx,
			created: time.Now(),
			shell:   shell,
		}
		go sess.readLoop()

		tm.mu.Lock()
		tm.sessions[id] = sess
		tm.mu.Unlock()

		return helpers.TextResult(fmt.Sprintf(`{"terminal_id":"%s","shell":"%s"}`, id, shell)), nil
	}
}

func (tm *TerminalManager) handleSendInput() func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "terminal_id", "data"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}
		terminalID := helpers.GetString(req.Arguments, "terminal_id")
		data := helpers.GetString(req.Arguments, "data")

		tm.mu.Lock()
		sess, ok := tm.sessions[terminalID]
		tm.mu.Unlock()
		if !ok {
			return helpers.ErrorResult("not_found", fmt.Sprintf("terminal %q not found", terminalID)), nil
		}

		if _, err := sess.ptmx.WriteString(data); err != nil {
			return helpers.ErrorResult("write_error", err.Error()), nil
		}
		return helpers.TextResult("ok"), nil
	}
}

func (tm *TerminalManager) handleTerminalOutput() func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "terminal_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}
		terminalID := helpers.GetString(req.Arguments, "terminal_id")

		tm.mu.Lock()
		sess, ok := tm.sessions[terminalID]
		tm.mu.Unlock()
		if !ok {
			return helpers.ErrorResult("not_found", fmt.Sprintf("terminal %q not found", terminalID)), nil
		}

		output := sess.drainOutput()
		// Return output as plain text — simpler for the client to parse.
		return helpers.TextResult(output), nil
	}
}

func (tm *TerminalManager) handleResizeTerminal() func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "terminal_id", "cols", "rows"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}
		terminalID := helpers.GetString(req.Arguments, "terminal_id")
		cols := helpers.GetInt(req.Arguments, "cols")
		rows := helpers.GetInt(req.Arguments, "rows")

		tm.mu.Lock()
		sess, ok := tm.sessions[terminalID]
		tm.mu.Unlock()
		if !ok {
			return helpers.ErrorResult("not_found", fmt.Sprintf("terminal %q not found", terminalID)), nil
		}

		if err := pty.Setsize(sess.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
			return helpers.ErrorResult("resize_error", err.Error()), nil
		}
		return helpers.TextResult("ok"), nil
	}
}

func (tm *TerminalManager) handleCloseTerminal() func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		if err := helpers.ValidateRequired(req.Arguments, "terminal_id"); err != nil {
			return helpers.ErrorResult("validation_error", err.Error()), nil
		}
		terminalID := helpers.GetString(req.Arguments, "terminal_id")

		tm.mu.Lock()
		sess, ok := tm.sessions[terminalID]
		if ok {
			delete(tm.sessions, terminalID)
		}
		tm.mu.Unlock()
		if !ok {
			return helpers.ErrorResult("not_found", fmt.Sprintf("terminal %q not found", terminalID)), nil
		}

		_ = sess.ptmx.Close()
		if sess.cmd.Process != nil {
			_ = sess.cmd.Process.Kill()
			_ = sess.cmd.Wait()
		}
		return helpers.TextResult("closed"), nil
	}
}

func (tm *TerminalManager) handleListTerminals() func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		tm.mu.Lock()
		defer tm.mu.Unlock()

		type info struct {
			ID    string `json:"id"`
			Shell string `json:"shell"`
		}
		var list []info
		for _, s := range tm.sessions {
			list = append(list, info{ID: s.id, Shell: s.shell})
		}
		data, _ := json.Marshal(list)
		return helpers.TextResult(string(data)), nil
	}
}
