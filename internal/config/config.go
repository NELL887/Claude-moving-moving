package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Source represents one Claude Code config root directory (containing a projects/ folder).
type Source struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	ConfigDir   string `json:"configDir"`   // e.g. ~/.claude or ~/.claude-work
	ProjectsDir string `json:"projectsDir"` // e.g. ~/.claude/projects
	Kind        string `json:"kind"`        // "default", "env", "wsl", "custom"
	Enabled     bool   `json:"enabled"`
	Exists      bool   `json:"exists"`
}

// AppConfig holds persistent user configuration in ~/.claude-session-bridge/config.json.
// Notice: It NEVER stores any credentials, cookies, or OAuth tokens.
type AppConfig struct {
	CustomSources []Source `json:"customSources"`
	Port          int      `json:"port"`
	Language      string   `json:"language"` // "zh" or "en"
	Theme         string   `json:"theme"`    // "dark" or "light"
}

func DefaultAppDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude-session-bridge"), nil
}

func ConfigFilePath() (string, error) {
	dir, err := DefaultAppDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (*AppConfig, error) {
	cfg := &AppConfig{
		Port:     18765,
		Language: "zh",
		Theme:    "dark",
	}
	p, err := ConfigFilePath()
	if err != nil {
		return cfg, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return cfg, err
	}
	if cfg.Port <= 0 {
		cfg.Port = 18765
	}
	if cfg.Language == "" {
		cfg.Language = "zh"
	}
	if cfg.Theme == "" {
		cfg.Theme = "dark"
	}
	return cfg, nil
}

func Save(cfg *AppConfig) error {
	p, err := ConfigFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// ExpandPath expands leading ~ to the user's home directory.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return filepath.Clean(p)
}

// DiscoverSources returns all auto-detected + user-configured Claude Code sources,
// deduplicated by normalized ProjectsDir path.
func DiscoverSources(cfg *AppConfig) []Source {
	seen := map[string]bool{}
	var out []Source

	add := func(s Source) {
		if s.ConfigDir == "" && s.ProjectsDir == "" {
			return
		}
		if s.ConfigDir != "" {
			s.ConfigDir = ExpandPath(s.ConfigDir)
		}
		if s.ProjectsDir == "" {
			// If user gave a directory already named "projects", use it directly.
			if strings.EqualFold(filepath.Base(s.ConfigDir), "projects") {
				s.ProjectsDir = s.ConfigDir
				s.ConfigDir = filepath.Dir(s.ConfigDir)
			} else {
				s.ProjectsDir = filepath.Join(s.ConfigDir, "projects")
			}
		} else {
			s.ProjectsDir = ExpandPath(s.ProjectsDir)
		}

		key := normalizeKey(s.ProjectsDir)
		if seen[key] {
			return
		}
		seen[key] = true

		if st, err := os.Stat(s.ProjectsDir); err == nil && st.IsDir() {
			s.Exists = true
		}
		if s.ID == "" {
			s.ID = makeSourceID(s.Kind, s.ConfigDir)
		}
		out = append(out, s)
	}

	home, _ := os.UserHomeDir()

	// 1. CLAUDE_CONFIG_DIR env var if set
	if envDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); envDir != "" {
		add(Source{
			ID:        "env-claude",
			Label:     "CLAUDE_CONFIG_DIR (" + filepath.Base(ExpandPath(envDir)) + ")",
			ConfigDir: envDir,
			Kind:      "env",
			Enabled:   true,
		})
	}

	// 2. Default ~/.claude
	if home != "" {
		defaultDir := filepath.Join(home, ".claude")
		add(Source{
			ID:        "default",
			Label:     "Default (~/.claude)",
			ConfigDir: defaultDir,
			Kind:      "default",
			Enabled:   true,
		})

		// 3. Auto-detect sibling multi-account folders like ~/.claude-*
		// (e.g. ~/.claude-work, ~/.claude-personal), excluding ~/.claude-session-bridge.
		if entries, err := os.ReadDir(home); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				if strings.HasPrefix(name, ".claude-") && name != ".claude-session-bridge" {
					cand := filepath.Join(home, name)
					if st, err := os.Stat(filepath.Join(cand, "projects")); err == nil && st.IsDir() {
						add(Source{
							Label:     "Profile (" + name + ")",
							ConfigDir: cand,
							Kind:      "auto-profile",
							Enabled:   true,
						})
					}
				}
			}
		}
	}

	// 4. On Windows, auto-probe WSL2 Ubuntu/Debian/etc. distributions via \\wsl.localhost or \\wsl$
	if runtime.GOOS == "windows" {
		for _, wslSrc := range discoverWindowsWSLSources() {
			add(wslSrc)
		}
	}

	// 5. User custom sources
	if cfg != nil {
		for _, cs := range cfg.CustomSources {
			if cs.Kind == "" {
				cs.Kind = "custom"
			}
			if cs.Label == "" {
				cs.Label = filepath.Base(cs.ConfigDir)
			}
			add(cs)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		order := map[string]int{"env": 0, "default": 1, "auto-profile": 2, "wsl": 3, "custom": 4}
		return order[out[i].Kind] < order[out[j].Kind]
	})

	return out
}

func discoverWindowsWSLSources() []Source {
	var found []Source
	roots := []string{`\\wsl.localhost`, `\\wsl$`}
	for _, root := range roots {
		distros, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, d := range distros {
			if !d.IsDir() {
				continue
			}
			homeBase := filepath.Join(root, d.Name(), "home")
			users, err := os.ReadDir(homeBase)
			if err != nil {
				continue
			}
			for _, u := range users {
				if !u.IsDir() {
					continue
				}
				cand := filepath.Join(homeBase, u.Name(), ".claude")
				if st, err := os.Stat(filepath.Join(cand, "projects")); err == nil && st.IsDir() {
					found = append(found, Source{
						Label:     "WSL (" + d.Name() + "/" + u.Name() + ")",
						ConfigDir: cand,
						Kind:      "wsl",
						Enabled:   true,
					})
				}
			}
		}
		if len(found) > 0 {
			break
		}
	}
	return found
}

func normalizeKey(p string) string {
	cleaned := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func makeSourceID(kind, configDir string) string {
	base := strings.ToLower(filepath.Base(configDir))
	var b strings.Builder
	b.WriteString(kind)
	b.WriteByte('-')
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// DefaultClaudeProjectsDir returns the default ~/.claude/projects directory.
func DefaultClaudeProjectsDir() string {
	if envDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); envDir != "" {
		return filepath.Join(ExpandPath(envDir), "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}
