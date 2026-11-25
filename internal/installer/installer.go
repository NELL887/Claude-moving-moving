package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"claude-session-bridge/internal/config"
)

const MCPServerKey = "claude-session-bridge"

// TargetStatus describes one detected Claude client MCP configuration target.
type TargetStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ConfigPath  string `json:"configPath"`
	FileExists  bool   `json:"fileExists"`
	ParentFound bool   `json:"parentFound"`
	Installed   bool   `json:"installed"`
	Command     string `json:"command,omitempty"`
}

// DetectTargets locates Claude Code CLI (~/.claude.json) and Claude Desktop config files on macOS/Windows/Linux.
// Never touches Keychain, Windows Credential Manager, or browser Cookies.
func DetectTargets() []TargetStatus {
	home, _ := os.UserHomeDir()
	var targets []TargetStatus

	// 1. Claude Code CLI + VS Code / Cursor Extension global user config (~/.claude.json)
	if home != "" {
		cliPath := filepath.Join(home, ".claude.json")
		targets = append(targets, inspectTarget("claude-cli", "Claude Code CLI & IDE Plugins (~/.claude.json)", cliPath))
	}

	// 2. Active CLAUDE_CONFIG_DIR/.claude.json if customized
	if envDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); envDir != "" {
		envPath := filepath.Join(config.ExpandPath(envDir), ".claude.json")
		if len(targets) == 0 || targets[0].ConfigPath != envPath {
			targets = append(targets, inspectTarget("claude-env", "CLAUDE_CONFIG_DIR (.claude.json)", envPath))
		}
	}

	// 3. Claude Desktop (Code tab + Chat)
	desktopPath := claudeDesktopConfigPath(home)
	if desktopPath != "" {
		targets = append(targets, inspectTarget("claude-desktop", "Claude Desktop App", desktopPath))
	}

	return targets
}

func claudeDesktopConfigPath(home string) string {
	switch runtime.GOOS {
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" && home != "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	}
}

func inspectTarget(id, name, cfgPath string) TargetStatus {
	st := TargetStatus{
		ID:         id,
		Name:       name,
		ConfigPath: cfgPath,
	}
	if _, err := os.Stat(filepath.Dir(cfgPath)); err == nil {
		st.ParentFound = true
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return st
	}
	st.FileExists = true
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return st
	}
	if servers, ok := root["mcpServers"].(map[string]any); ok {
		if entry, ok := servers[MCPServerKey].(map[string]any); ok {
			st.Installed = true
			if cmd, ok := entry["command"].(string); ok {
				st.Command = cmd
			}
		}
	}
	return st
}

// InstallMCP safely backs up the target JSON config and injects `claude-session-bridge` under mcpServers.
func InstallMCP(targetID, binaryPath string) (*TargetStatus, string, error) {
	if binaryPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, "", err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		binaryPath = exe
	}

	targets := DetectTargets()
	var chosen *TargetStatus
	for i := range targets {
		if targets[i].ID == targetID {
			chosen = &targets[i]
			break
		}
	}
	if chosen == nil {
		return nil, "", fmt.Errorf("unknown target: %s", targetID)
	}

	if err := os.MkdirAll(filepath.Dir(chosen.ConfigPath), 0o755); err != nil {
		return nil, "", err
	}

	root := map[string]any{}
	backupPath := ""
	if data, err := os.ReadFile(chosen.ConfigPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, "", fmt.Errorf("existing config %s is not valid JSON: %w", chosen.ConfigPath, err)
		}
		backupPath = chosen.ConfigPath + ".csb-bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backupPath, data, 0o600); err != nil {
			return nil, "", fmt.Errorf("failed to create backup: %w", err)
		}
	}

	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		servers = map[string]any{}
	}
	servers[MCPServerKey] = map[string]any{
		"command": binaryPath,
		"args":    []string{"mcp"},
	}
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, backupPath, err
	}
	if err := os.WriteFile(chosen.ConfigPath, append(out, '\n'), 0o600); err != nil {
		return nil, backupPath, err
	}

	updated := inspectTarget(chosen.ID, chosen.Name, chosen.ConfigPath)
	return &updated, backupPath, nil
}

// UninstallMCP safely backs up the target JSON config and removes only `claude-session-bridge`.
func UninstallMCP(targetID string) (*TargetStatus, string, error) {
	targets := DetectTargets()
	var chosen *TargetStatus
	for i := range targets {
		if targets[i].ID == targetID {
			chosen = &targets[i]
			break
		}
	}
	if chosen == nil {
		return nil, "", fmt.Errorf("unknown target: %s", targetID)
	}
	data, err := os.ReadFile(chosen.ConfigPath)
	if err != nil {
		return chosen, "", nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, "", err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return chosen, "", nil
	}
	if _, exists := servers[MCPServerKey]; !exists {
		return chosen, "", nil
	}

	backupPath := chosen.ConfigPath + ".csb-bak-" + time.Now().Format("20060102-150405")
	_ = os.WriteFile(backupPath, data, 0o600)

	delete(servers, MCPServerKey)
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, backupPath, err
	}
	if err := os.WriteFile(chosen.ConfigPath, append(out, '\n'), 0o600); err != nil {
		return nil, backupPath, err
	}
	updated := inspectTarget(chosen.ID, chosen.Name, chosen.ConfigPath)
	return &updated, backupPath, nil
}
