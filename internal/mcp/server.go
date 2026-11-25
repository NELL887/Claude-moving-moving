package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"claude-session-bridge/internal/sanitizer"
	"claude-session-bridge/internal/store"
)

const (
	ServerName    = "claude-session-bridge"
	ServerVersion = "1.0.0"
	ProtocolVer   = "2024-11-05"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server serves Claude Session Bridge tools over standard MCP stdio transport.
type Server struct {
	store *store.Store
	in    io.Reader
	out   io.Writer
}

func NewServer(st *store.Store) *Server {
	return &Server{
		store: st,
		in:    os.Stdin,
		out:   os.Stdout,
	}
}

func NewServerIO(st *store.Store, in io.Reader, out io.Writer) *Server {
	return &Server{
		store: st,
		in:    in,
		out:   out,
	}
}

// Serve runs the stdio JSON-RPC loop until EOF.
func (s *Server) Serve() error {
	_, _ = s.store.Refresh()

	reader := bufio.NewReaderSize(s.in, 1024*1024)
	writer := bufio.NewWriter(s.out)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				// Ignore optional LSP-style headers if any client sends them
				if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
					continue
				}
				var req rpcRequest
				if jerr := json.Unmarshal([]byte(trimmed), &req); jerr != nil {
					_ = writeResp(writer, rpcResponse{
						JSONRPC: "2.0",
						ID:      json.RawMessage("null"),
						Error:   &rpcError{Code: -32700, Message: "parse error: " + jerr.Error()},
					})
					continue
				}
				if resp := s.handleRequest(&req); resp != nil {
					_ = writeResp(writer, *resp)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func writeResp(w *bufio.Writer, resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

func (s *Server) handleRequest(req *rpcRequest) *rpcResponse {
	// Notifications have no ID and expect no response
	if len(req.ID) == 0 || string(req.ID) == "null" {
		if req.Method == "notifications/initialized" || req.Method == "notifications/cancelled" {
			return nil
		}
	}

	switch req.Method {
	case "initialize":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": ProtocolVer,
				"capabilities": map[string]any{
					"tools": map[string]any{
						"listChanged": false,
					},
				},
				"serverInfo": map[string]any{
					"name":    ServerName,
					"version": ServerVersion,
				},
			},
		}

	case "ping":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": toolDefinitions(),
			},
		}

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return &rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid params: " + err.Error()},
			}
		}
		text, isErr := s.callTool(p.Name, p.Arguments)
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": text},
				},
				"isError": isErr,
			},
		}

	default:
		if len(req.ID) == 0 {
			return nil
		}
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (s *Server) callTool(name string, args map[string]any) (string, bool) {
	// Refresh incrementally so newly saved sessions from other accounts appear immediately
	_, _ = s.store.Refresh()

	switch name {
	case "list_recent_sessions":
		projectHint := argString(args, "project_hint")
		sourceID := argString(args, "source_id")
		limit := argInt(args, "limit", 15)
		if limit > 50 {
			limit = 50
		}
		sessions := s.store.ListSessions(sourceID, "", projectHint, limit)
		if len(sessions) == 0 {
			return "No matching local Claude Code sessions found.", false
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Found %d local Claude Code session(s):\n\n", len(sessions)))
		for i, sum := range sessions {
			b.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, sum.Title))
			b.WriteString(fmt.Sprintf("   - `session_id`: `%s`\n", sum.SessionID))
			b.WriteString(fmt.Sprintf("   - Project: `%s` (cwd: `%s`, source: `%s`)\n", sum.ProjectName, sum.Cwd, sum.SourceLabel))
			b.WriteString(fmt.Sprintf("   - Updated: %s | Turns: %d | Est. Tokens: ~%d\n",
				sum.UpdatedAt.Local().Format("2006-01-02 15:04"), sum.TurnCount, sum.EstimatedToks))
			if len(sum.TouchedFiles) > 0 {
				maxF := len(sum.TouchedFiles)
				if maxF > 5 {
					maxF = 5
				}
				b.WriteString(fmt.Sprintf("   - Files: %s\n", strings.Join(sum.TouchedFiles[:maxF], ", ")))
			}
		}
		return b.String(), false

	case "search_sessions":
		query := argString(args, "query")
		if query == "" {
			return "Missing required parameter: query", true
		}
		projectHint := argString(args, "project_hint")
		limit := argInt(args, "limit", 10)
		if limit > 30 {
			limit = 30
		}
		hits := s.store.Search(query, "", "", projectHint, limit)
		if len(hits) == 0 {
			return fmt.Sprintf("No local sessions matched query %q.", query), false
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Search results for %q (%d hit(s)):\n\n", query, len(hits)))
		for i, h := range hits {
			b.WriteString(fmt.Sprintf("%d. **%s** (`%s`)\n", i+1, h.Session.Title, h.Session.SessionID))
			b.WriteString(fmt.Sprintf("   - Project: `%s` | Source: `%s` | Updated: %s\n",
				h.Session.ProjectName, h.Session.SourceLabel, h.Session.UpdatedAt.Local().Format("2006-01-02 15:04")))
			b.WriteString(fmt.Sprintf("   - Match: %s\n", h.Snippet))
		}
		return b.String(), false

	case "get_session_handoff":
		sid := argString(args, "session_id")
		if sid == "" {
			return "Missing required parameter: session_id", true
		}
		level := argString(args, "detail_level")
		if level == "" {
			level = "standard"
		}
		detail, err := s.store.GetSessionDetail(sid)
		if err != nil {
			return "Error loading session: " + err.Error(), true
		}
		return sanitizer.GenerateHandoff(detail, level), false

	case "get_session_turns":
		sid := argString(args, "session_id")
		if sid == "" {
			return "Missing required parameter: session_id", true
		}
		startTurn := argInt(args, "start_turn", 1)
		endTurn := argInt(args, "end_turn", startTurn+9)
		if startTurn < 1 {
			startTurn = 1
		}
		detail, err := s.store.GetSessionDetail(sid)
		if err != nil {
			return "Error loading session: " + err.Error(), true
		}
		total := len(detail.Turns)
		if total == 0 {
			return "Session contains no conversational turns.", false
		}
		sIdx := startTurn - 1
		eIdx := endTurn
		if sIdx >= total {
			return fmt.Sprintf("start_turn %d exceeds total turns (%d).", startTurn, total), true
		}
		if eIdx > total {
			eIdx = total
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Session `%s` — Turns %d..%d of %d:\n\n", detail.Summary.SessionID, sIdx+1, eIdx, total))
		for _, t := range detail.Turns[sIdx:eIdx] {
			b.WriteString(fmt.Sprintf("### Turn #%d (%s)\n", t.Index+1, t.Role))
			if t.PlainText != "" {
				b.WriteString(t.PlainText + "\n\n")
			}
			for _, blk := range t.Blocks {
				if blk.Type == "tool_use" {
					b.WriteString(fmt.Sprintf("- Tool `%s`: `%s`\n", blk.ToolName, strings.ReplaceAll(blk.ToolInput, "\n", " ")))
				}
			}
			b.WriteByte('\n')
		}
		return b.String(), false

	case "clean_fork_session":
		sid := argString(args, "session_id")
		if sid == "" {
			return "Missing required parameter: session_id", true
		}
		sum, ok := s.store.GetSessionSummary(sid)
		if !ok {
			return "Session not found: " + sid, true
		}
		res, err := sanitizer.CleanForkSession(sanitizer.CleanForkOptions{
			SourceFilePath:  sum.FilePath,
			TargetConfigDir: argString(args, "target_config_dir"),
			TargetCwd:       argString(args, "target_cwd"),
			NewTitle:        argString(args, "new_title"),
		})
		if err != nil {
			return "Clean fork failed: " + err.Error(), true
		}
		_, _ = s.store.Refresh()
		out, _ := json.MarshalIndent(res, "", "  ")
		return "Clean fork created successfully (thinking signatures & account metadata stripped):\n```json\n" + string(out) + "\n```", false

	default:
		return "Unknown tool: " + name, true
	}
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_recent_sessions",
			"description": "List recent local Claude Code conversations across all local accounts, profiles, and projects (read-only, sanitized).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_hint": map[string]any{"type": "string", "description": "Optional substring to filter by project name, cwd, or git remote"},
					"source_id":    map[string]any{"type": "string", "description": "Optional source profile ID filter"},
					"limit":        map[string]any{"type": "integer", "description": "Max sessions to return (default 15, max 50)"},
				},
			},
		},
		{
			"name":        "search_sessions",
			"description": "Full-text search across all local Claude Code conversation histories (supports Chinese and English).",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":        map[string]any{"type": "string", "description": "Search keywords (e.g. error message, feature name, filename)"},
					"project_hint": map[string]any{"type": "string", "description": "Optional project filter"},
					"limit":        map[string]any{"type": "integer", "description": "Max results (default 10)"},
				},
			},
		},
		{
			"name":        "get_session_handoff",
			"description": "Generate a clean, structured handoff context for a local Claude Code session so the current account can seamlessly continue work from another account's session.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id":   map[string]any{"type": "string", "description": "Session UUID (or unique prefix) from list_recent_sessions / search_sessions"},
					"detail_level": map[string]any{"type": "string", "enum": []string{"brief", "standard", "full"}, "description": "brief = minimal tokens, standard = balanced (default), full = full sanitized transcript"},
				},
			},
		},
		{
			"name":        "get_session_turns",
			"description": "Read a specific 1-based range of turns from a local Claude Code session (sanitized, without thinking signatures).",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string", "description": "Session UUID"},
					"start_turn": map[string]any{"type": "integer", "description": "1-based start turn index (default 1)"},
					"end_turn":   map[string]any{"type": "integer", "description": "1-based inclusive end turn index"},
				},
			},
		},
		{
			"name":        "clean_fork_session",
			"description": "Create a sanitized Clean Fork copy of a session .jsonl (strips thinking signatures and server metadata, rewrites UUID/cwd) for native `claude --resume`.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id":        map[string]any{"type": "string", "description": "Source session UUID"},
					"target_cwd":        map[string]any{"type": "string", "description": "Optional target project directory path"},
					"target_config_dir": map[string]any{"type": "string", "description": "Optional target CLAUDE_CONFIG_DIR"},
					"new_title":         map[string]any{"type": "string", "description": "Optional title for the forked session"},
				},
			},
		},
	}
}

func argString(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argInt(m map[string]any, k string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[k].(type) {
	case float64:
		if int(v) > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return def
}
