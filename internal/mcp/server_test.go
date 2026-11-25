package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-session-bridge/internal/config"
	"claude-session-bridge/internal/store"
)

func TestMCPStdioLifecycleAndTools(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "claude-a", "projects", "-Users-demo-shop")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sample := strings.Join([]string{
		`{"type":"user","sessionId":"11111111-2222-3333-4444-555555555555","uuid":"u1","cwd":"/Users/demo/shop","timestamp":"2026-11-24T12:00:00Z","message":{"role":"user","content":"Implement Stripe webhook verification"}}`,
		`{"type":"assistant","sessionId":"11111111-2222-3333-4444-555555555555","uuid":"a1","parentUuid":"u1","cwd":"/Users/demo/shop","timestamp":"2026-11-24T12:00:10Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"SIG123"},{"type":"text","text":"Added HMAC SHA-256 verification in webhook.go."}]}}`,
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(projDir, "11111111-2222-3333-4444-555555555555.jsonl"), []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.AppConfig{
		CustomSources: []config.Source{
			{ID: "acct-a", Label: "Account A", ConfigDir: filepath.Join(tmp, "claude-a"), Kind: "custom", Enabled: true},
		},
	}
	st := store.New(cfg)

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_sessions","arguments":{"query":"Stripe webhook"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_session_handoff","arguments":{"session_id":"11111111-2222-3333-4444-555555555555","detail_level":"standard"}}}`,
	}

	var out bytes.Buffer
	srv := NewServerIO(st, strings.NewReader(strings.Join(requests, "\n")+"\n"), &out)
	if err := srv.Serve(); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 responses (notification produces none), got %d:\n%s", len(lines), out.String())
	}

	// Verify handoff output contains expected text and NO signature
	var resp4 map[string]any
	if err := json.Unmarshal([]byte(lines[3]), &resp4); err != nil {
		t.Fatal(err)
	}
	raw4 := lines[3]
	if !strings.Contains(raw4, "Stripe webhook verification") || !strings.Contains(raw4, "webhook.go") {
		t.Errorf("handoff response missing session text: %s", raw4)
	}
	if strings.Contains(raw4, "SIG123") {
		t.Errorf("handoff response leaked thinking signature!")
	}
}
