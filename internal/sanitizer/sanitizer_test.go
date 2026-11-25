package sanitizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-session-bridge/internal/parser"
)

func sampleJSONL() string {
	return strings.Join([]string{
		`{"type":"api-request","sessionId":"old-sess-1","id":"req_secret"}`,
		`{"type":"atis-latch","sessionId":"old-sess-1","atis":true}`,
		`{"type":"user","sessionId":"old-sess-1","uuid":"u1","cwd":"/Users/alice/my-app","gitBranch":"main","requestId":"req_1","serverClassifierContext":"ctx","timestamp":"2026-11-24T10:00:00Z","message":{"role":"user","content":"帮我修复登录接口的 500 报错"}}`,
		`{"type":"assistant","sessionId":"old-sess-1","uuid":"a1","parentUuid":"u1","cwd":"/Users/alice/my-app","requestId":"req_2","wireIngestContext":"secret","timestamp":"2026-11-24T10:00:05Z","message":{"id":"msg_123","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"internal thought","signature":"ENCRYPTED_SIG_ACCOUNT_A"},{"type":"text","text":"我先查看 auth.go 中的空指针问题。"},{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"/Users/alice/my-app/auth.go"}}]}}`,
		`{"type":"user","sessionId":"old-sess-1","uuid":"u2","parentUuid":"a1","cwd":"/Users/alice/my-app","timestamp":"2026-11-24T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"package auth\nfunc Login() {}"}]}}`,
	}, "\n") + "\n"
}

func TestCleanForkAndBundleRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "old-sess-1.jsonl")
	if err := os.WriteFile(srcFile, []byte(sampleJSONL()), 0o600); err != nil {
		t.Fatal(err)
	}

	targetCfg := filepath.Join(tmp, "target-claude")
	targetCwd := `D:\Work\my-app`

	res, err := CleanForkSession(CleanForkOptions{
		SourceFilePath:  srcFile,
		TargetConfigDir: targetCfg,
		TargetCwd:       targetCwd,
		NewTitle:        "Migrated Login Fix",
	})
	if err != nil {
		t.Fatalf("CleanForkSession failed: %v", err)
	}
	if res.ThinkingBlocksDrop != 1 {
		t.Fatalf("expected 1 thinking block dropped, got %d", res.ThinkingBlocksDrop)
	}
	if res.InternalRecordsDrop != 2 {
		t.Fatalf("expected 2 internal records dropped, got %d", res.InternalRecordsDrop)
	}
	if res.EncodedProject != "D--Work-my-app" {
		t.Fatalf("unexpected EncodedProject: %s", res.EncodedProject)
	}

	outBytes, err := os.ReadFile(res.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	outText := string(outBytes)
	for _, forbidden := range []string{"ENCRYPTED_SIG_ACCOUNT_A", "api-request", "atis-latch", "wireIngestContext", "serverClassifierContext", "old-sess-1"} {
		if strings.Contains(outText, forbidden) {
			t.Errorf("sanitized output must NOT contain %q", forbidden)
		}
	}
	if !strings.Contains(outText, "帮我修复登录接口的 500 报错") || !strings.Contains(outText, "Migrated Login Fix") {
		t.Errorf("sanitized output missing expected conversation content")
	}

	// Test portable bundle export + import round-trip
	parsed, err := parser.ParseSessionFile(srcFile, "src-a", "Account A", "-Users-alice-my-app", true)
	if err != nil {
		t.Fatal(err)
	}
	detail := &parser.SessionDetail{Summary: parsed.Summary, Turns: parsed.Turns}

	bundleBytes, err := ExportBundleBytes(detail)
	if err != nil {
		t.Fatalf("ExportBundleBytes failed: %v", err)
	}

	importCfg := filepath.Join(tmp, "imported-claude")
	impRes, manifest, err := ImportBundleBytes(bundleBytes, importCfg, "/Users/bob/my-app")
	if err != nil {
		t.Fatalf("ImportBundleBytes failed: %v", err)
	}
	if manifest.ProjectName != "my-app" {
		t.Fatalf("unexpected manifest projectName: %q", manifest.ProjectName)
	}
	if impRes.EncodedProject != "-Users-bob-my-app" {
		t.Fatalf("unexpected imported EncodedProject: %q", impRes.EncodedProject)
	}
	if _, err := os.Stat(impRes.OutputPath); err != nil {
		t.Fatalf("imported file missing: %v", err)
	}
}
