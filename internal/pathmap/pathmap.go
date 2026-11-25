package pathmap

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// EncodeProjectPath replicates Claude Code's project folder encoding:
// every non-alphanumeric character (/, \, :, space, ., _, etc.) is replaced with '-'.
// Example:
//
//	/Users/nell/code/my-app -> -Users-nell-code-my-app
//	C:\Users\nell\my-app    -> C--Users-nell-my-app
func EncodeProjectPath(absPath string) string {
	p := strings.TrimSpace(absPath)
	if p == "" {
		return "unknown-project"
	}
	// Trim trailing slashes unless it's root "/" or "C:\"
	if len(p) > 1 {
		p = strings.TrimRight(p, `/\`)
	}
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// LogicalProjectKey derives a cross-OS project grouping key so that the same repository
// on macOS (/Users/a/my-app) and Windows (D:\Work\my-app) can be recognized as the same project.
// Priority:
//  1. Normalized git remote URL (if cwd exists locally and has a .git/config)
//  2. Normalized basename of cwd (if cwd is known)
//  3. Encoded folder name fallback
func LogicalProjectKey(cwd string, encodedFolder string) (key string, displayName string, gitRemote string) {
	if cwd != "" {
		gitRemote = DetectGitRemote(cwd)
		displayName = BaseNameCrossOS(cwd)
		if gitRemote != "" {
			return "git:" + gitRemote, displayName, gitRemote
		}
		if displayName != "" {
			return "dir:" + strings.ToLower(displayName), displayName, ""
		}
	}
	displayName = InferNameFromEncoded(encodedFolder)
	return "enc:" + strings.ToLower(encodedFolder), displayName, ""
}

// BaseNameCrossOS extracts the last path segment for both Unix and Windows paths.
func BaseNameCrossOS(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimRight(p, `/\`)
	if p == "" {
		return ""
	}
	idx := strings.LastIndexAny(p, `/\`)
	if idx >= 0 && idx+1 < len(p) {
		return p[idx+1:]
	}
	return p
}

// InferNameFromEncoded gives a readable fallback name when cwd wasn't recorded in a session.
func InferNameFromEncoded(encoded string) string {
	trimmed := strings.Trim(encoded, "-")
	if trimmed == "" {
		return "Unknown Project"
	}
	parts := strings.Split(trimmed, "-")
	if len(parts) <= 2 {
		return trimmed
	}
	// Take the last 2 segments as a readable hint
	return strings.Join(parts[len(parts)-2:], "-")
}

// DetectGitRemote reads .git/config (without shelling out to git) to find origin remote URL.
func DetectGitRemote(dir string) string {
	if dir == "" {
		return ""
	}
	gitDir := filepath.Join(dir, ".git")
	st, err := os.Stat(gitDir)
	if err != nil {
		return ""
	}
	cfgPath := filepath.Join(gitDir, "config")
	if !st.IsDir() {
		// Worktree/submodule: .git is a file pointing to "gitdir: ..."
		data, err := os.ReadFile(gitDir)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		if strings.HasPrefix(line, "gitdir:") {
			target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
			if !filepath.IsAbs(target) {
				target = filepath.Join(dir, target)
			}
			cfgPath = filepath.Join(target, "config")
		}
	}
	f, err := os.Open(cfgPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	inOrigin := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(line, `[remote "origin"]`)
			continue
		}
		if inOrigin && strings.HasPrefix(strings.ToLower(line), "url") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return NormalizeGitRemote(strings.TrimSpace(parts[1]))
			}
		}
	}
	return ""
}

// NormalizeGitRemote canonicalizes SSH/HTTPS git remotes to host/owner/repo.
func NormalizeGitRemote(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "ssh://")
	if strings.HasPrefix(s, "git@") {
		s = strings.TrimPrefix(s, "git@")
		s = strings.Replace(s, ":", "/", 1)
	}
	return strings.ToLower(strings.Trim(s, "/"))
}
