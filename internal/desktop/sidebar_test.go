package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectAndPushToDesktopSidebar(t *testing.T) {
	tmp := t.TempDir()

	// Create mock claude-code-sessions with two accounts
	root := filepath.Join(tmp, "claude-code-sessions")
	wsOld := filepath.Join(root, "acc-old", "org-old")
	wsNew := filepath.Join(root, "acc-new", "org-new")
	if err := os.MkdirAll(wsOld, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wsNew, 0o755); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-48 * time.Hour).UnixMilli()
	newTime := time.Now().Add(-5 * time.Minute).UnixMilli()
	_ = os.WriteFile(filepath.Join(wsOld, "local_111.json"), []byte(`{"title":"Old Session","lastActivityAt":`+jsonNum(oldTime)+`}`), 0o600)
	_ = os.WriteFile(filepath.Join(wsNew, "local_222.json"), []byte(`{"title":"Recent New Account Session","lastActivityAt":`+jsonNum(newTime)+`}`), 0o600)

	detected := DetectWorkspacesInRoot(root)
	if len(detected) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(detected))
	}
	if !detected[0].IsMostRecent || detected[0].AccountUUID != "acc-new" {
		t.Fatalf("expected acc-new to be most recent, got %+v", detected[0])
	}

	// Create mock source .jsonl with thinking signature
	srcJSONL := filepath.Join(tmp, "src.jsonl")
	sample := strings.Join([]string{
		`{"type":"user","sessionId":"old-sid","uuid":"u1","cwd":"/Users/demo/app","timestamp":"2026-11-24T10:00:00Z","message":{"role":"user","content":"Fix bug"}}`,
		`{"type":"assistant","sessionId":"old-sid","uuid":"a1","parentUuid":"u1","cwd":"/Users/demo/app","timestamp":"2026-11-24T10:00:05Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"SIG_OLD"},{"type":"text","text":"Fixed."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(srcJSONL, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	targetCfg := filepath.Join(tmp, "claude-cfg")
	res, err := PushToDesktopSidebar(PushOptions{
		SourceFilePath:  srcJSONL,
		WorkspaceDir:    wsNew,
		TargetConfigDir: targetCfg,
		TargetCwd:       "/Users/demo/app",
		Title:           "Transferred Fix",
	})
	if err != nil {
		t.Fatalf("PushToDesktopSidebar failed: %v", err)
	}

	// Verify sidebar index JSON points to clean cliSessionId
	rawIdx, err := os.ReadFile(res.SidebarIndexPath)
	if err != nil {
		t.Fatal(err)
	}
	var idxMap map[string]any
	if err := json.Unmarshal(rawIdx, &idxMap); err != nil {
		t.Fatal(err)
	}
	if idxMap["cliSessionId"] != res.CLISessionID {
		t.Fatalf("cliSessionId mismatch: %v != %s", idxMap["cliSessionId"], res.CLISessionID)
	}
	if idxMap["title"] != "Transferred Fix" {
		t.Fatalf("unexpected title: %v", idxMap["title"])
	}

	// Verify forked .jsonl stripped SIG_OLD
	forkData, err := os.ReadFile(res.ForkResult.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(forkData), "SIG_OLD") {
		t.Fatalf("forked jsonl leaked thinking signature!")
	}
}

func jsonNum(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
