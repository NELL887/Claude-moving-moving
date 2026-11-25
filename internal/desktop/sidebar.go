package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"claude-session-bridge/internal/sanitizer"
)

// Workspace represents one <account_uuid>/<org_uuid> folder under Claude Desktop's claude-code-sessions.
// Note: We only inspect folder names and local_*.json session titles/timestamps — NEVER cookies or tokens.
type Workspace struct {
	ID            string    `json:"id"` // accountUUID/orgUUID
	AccountUUID   string    `json:"accountUuid"`
	OrgUUID       string    `json:"orgUuid"`
	DirPath       string    `json:"dirPath"`
	SessionCount  int       `json:"sessionCount"`
	LatestTitle   string    `json:"latestTitle,omitempty"`
	LastActivity  time.Time `json:"lastActivity"`
	IsMostRecent  bool      `json:"isMostRecent"`
}

// PushOptions configures pushing a sanitized session into Claude Desktop's Code sidebar.
type PushOptions struct {
	SourceFilePath  string `json:"sourceFilePath"`
	WorkspaceDir    string `json:"workspaceDir"`    // Target claude-code-sessions/<account>/<org> directory
	TargetConfigDir string `json:"targetConfigDir"` // Target Claude CLI config dir (empty = default ~/.claude)
	TargetCwd       string `json:"targetCwd"`
	Title           string `json:"title"`
	Model           string `json:"model"`
}

// PushResult reports the created Clean Fork .jsonl and Desktop sidebar index file.
type PushResult struct {
	LocalSessionID   string                   `json:"localSessionId"`
	CLISessionID     string                   `json:"cliSessionId"`
	SidebarIndexPath string                   `json:"sidebarIndexPath"`
	ForkResult       *sanitizer.CleanForkResult `json:"forkResult"`
}

// SessionsRootDir returns Claude Desktop's claude-code-sessions directory for the current OS.
func SessionsRootDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" && home != "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Claude", "claude-code-sessions")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Claude", "claude-code-sessions")
	}
}

// DetectWorkspaces scans the default Claude Desktop claude-code-sessions directory.
func DetectWorkspaces() []Workspace {
	return DetectWorkspacesInRoot(SessionsRootDir())
}

// DetectWorkspacesInRoot scans a given claude-code-sessions root directory (exported for testing).
func DetectWorkspacesInRoot(root string) []Workspace {
	if root == "" {
		return nil
	}
	accounts, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var out []Workspace
	for _, acc := range accounts {
		if !acc.IsDir() || strings.HasPrefix(acc.Name(), ".") {
			continue
		}
		accDir := filepath.Join(root, acc.Name())
		orgs, err := os.ReadDir(accDir)
		if err != nil {
			continue
		}
		for _, org := range orgs {
			if !org.IsDir() || strings.HasPrefix(org.Name(), ".") {
				continue
			}
			wsDir := filepath.Join(accDir, org.Name())
			ws := inspectWorkspace(acc.Name(), org.Name(), wsDir)
			out = append(out, ws)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActivity.After(out[j].LastActivity)
	})
	if len(out) > 0 {
		out[0].IsMostRecent = true
	}
	return out
}

func inspectWorkspace(accountUUID, orgUUID, wsDir string) Workspace {
	ws := Workspace{
		ID:          accountUUID + "/" + orgUUID,
		AccountUUID: accountUUID,
		OrgUUID:     orgUUID,
		DirPath:     wsDir,
	}
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		return ws
	}

	var latestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "local_") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ws.SessionCount++
		info, err := e.Info()
		if err != nil {
			continue
		}
		fullPath := filepath.Join(wsDir, e.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		var meta struct {
			Title          string `json:"title"`
			LastActivityAt int64  `json:"lastActivityAt"`
		}
		_ = json.Unmarshal(data, &meta)
		t := info.ModTime()
		if meta.LastActivityAt > 0 {
			t = time.UnixMilli(meta.LastActivityAt)
		}
		if t.After(latestMod) {
			latestMod = t
			ws.LatestTitle = meta.Title
		}
	}
	if latestMod.IsZero() {
		if st, err := os.Stat(wsDir); err == nil {
			latestMod = st.ModTime()
		}
	}
	ws.LastActivity = latestMod.UTC()
	return ws
}

// PushToDesktopSidebar creates a sanitized Clean Fork .jsonl and writes a matching
// local_<uuid>.json entry into the target Claude Desktop workspace folder.
func PushToDesktopSidebar(opts PushOptions) (*PushResult, error) {
	if opts.SourceFilePath == "" {
		return nil, fmt.Errorf("sourceFilePath is required")
	}
	if opts.WorkspaceDir == "" {
		return nil, fmt.Errorf("workspaceDir is required")
	}
	if st, err := os.Stat(opts.WorkspaceDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("target workspace directory does not exist: %s", opts.WorkspaceDir)
	}

	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "Migrated Session"
	}

	// 1. Create a sanitized Clean Fork .jsonl (strips thinking signatures & account metadata)
	forkRes, err := sanitizer.CleanForkSession(sanitizer.CleanForkOptions{
		SourceFilePath:  opts.SourceFilePath,
		TargetConfigDir: opts.TargetConfigDir,
		TargetCwd:       opts.TargetCwd,
		NewTitle:        title,
	})
	if err != nil {
		return nil, err
	}

	// 2. Write Claude Desktop local_<uuid>.json index entry pointing to forkRes.NewSessionID
	localID := "local_" + sanitizer.NewUUIDv4()
	nowMs := time.Now().UnixMilli()
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = "claude-sonnet-4-5"
	}

	sidebarDoc := map[string]any{
		"sessionId":            localID,
		"cliSessionId":         forkRes.NewSessionID,
		"cwd":                  forkRes.TargetCwd,
		"originCwd":            forkRes.TargetCwd,
		"title":                title,
		"titleSource":          "user",
		"model":                model,
		"createdAt":            nowMs,
		"lastActivityAt":       nowMs,
		"lastFocusedAt":        nowMs,
		"isArchived":           false,
		"permissionMode":       "default",
		"chromePermissionMode": "default",
	}

	data, err := json.MarshalIndent(sidebarDoc, "", "  ")
	if err != nil {
		return nil, err
	}

	indexPath := filepath.Join(opts.WorkspaceDir, localID+".json")
	if err := os.WriteFile(indexPath, append(data, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write desktop sidebar index: %w", err)
	}

	return &PushResult{
		LocalSessionID:   localID,
		CLISessionID:     forkRes.NewSessionID,
		SidebarIndexPath: indexPath,
		ForkResult:       forkRes,
	}, nil
}
