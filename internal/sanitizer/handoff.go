package sanitizer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"claude-session-bridge/internal/parser"
)

// HandoffLevel controls how much detail is included in a structured cross-account handoff document.
//   - "brief":    Goal + touched files + last 3 turns (minimal tokens)
//   - "standard": Goal + touched files + key assistant decisions + last 8 turns (default)
//   - "full":     Full sanitized conversation transcript
func GenerateHandoff(detail *parser.SessionDetail, level string) string {
	if level == "" {
		level = "standard"
	}
	sum := detail.Summary
	var b strings.Builder

	b.WriteString("# Claude Code Session Handoff\n\n")
	b.WriteString("> Generated locally by **Claude Session Bridge** (sanitized, zero credentials/signatures).\n\n")

	b.WriteString("## 1. Session Metadata\n")
	b.WriteString(fmt.Sprintf("- **Title**: %s\n", sum.Title))
	b.WriteString(fmt.Sprintf("- **Project**: %s\n", sum.ProjectName))
	if sum.Cwd != "" {
		b.WriteString(fmt.Sprintf("- **Working Directory**: `%s`\n", sum.Cwd))
	}
	if sum.GitBranch != "" {
		b.WriteString(fmt.Sprintf("- **Git Branch**: `%s`\n", sum.GitBranch))
	}
	if sum.GitRemote != "" {
		b.WriteString(fmt.Sprintf("- **Git Remote**: `%s`\n", sum.GitRemote))
	}
	b.WriteString(fmt.Sprintf("- **Original Session ID**: `%s` (Source: %s)\n", sum.SessionID, sum.SourceLabel))
	b.WriteString(fmt.Sprintf("- **Updated At**: %s | **Turns**: %d (User: %d, Assistant: %d)\n\n",
		sum.UpdatedAt.Local().Format("2006-01-02 15:04:05"), sum.TurnCount, sum.UserCount, sum.AssistantCount))

	// 2. Primary Goal & User Requests
	b.WriteString("## 2. Original Goal & Key User Requests\n")
	userPrompts := collectMeaningfulUserTurns(detail.Turns)
	if len(userPrompts) == 0 {
		b.WriteString("- *(No explicit text prompt recorded)*\n")
	} else {
		maxPrompts := 6
		if level == "brief" {
			maxPrompts = 3
		} else if level == "full" {
			maxPrompts = len(userPrompts)
		}
		for i, u := range userPrompts {
			if i >= maxPrompts && i < len(userPrompts)-1 {
				if i == maxPrompts {
					b.WriteString(fmt.Sprintf("- *(... %d intermediate user messages omitted ...)*\n", len(userPrompts)-maxPrompts-1))
				}
				continue
			}
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, clipText(u.PlainText, 400)))
		}
	}
	b.WriteByte('\n')

	// 3. Touched Files & Tools
	if len(sum.TouchedFiles) > 0 || len(sum.ToolsUsed) > 0 {
		b.WriteString("## 3. Files & Tools Referenced\n")
		if len(sum.ToolsUsed) > 0 {
			b.WriteString(fmt.Sprintf("- **Tools Used**: `%s`\n", strings.Join(sum.ToolsUsed, "`, `")))
		}
		if len(sum.TouchedFiles) > 0 {
			b.WriteString("- **Files Read/Modified**:\n")
			limit := 25
			if level == "brief" {
				limit = 10
			}
			for i, f := range sum.TouchedFiles {
				if i >= limit {
					b.WriteString(fmt.Sprintf("  - *(+%d more files)*\n", len(sum.TouchedFiles)-limit))
					break
				}
				b.WriteString(fmt.Sprintf("  - `%s`\n", f))
			}
		}
		b.WriteByte('\n')
	}

	// 4. Recent / Full Conversation Context
	b.WriteString("## 4. Conversation Context to Continue From\n\n")
	turns := detail.Turns
	startIdx := 0
	maxPerTurn := 2000
	switch level {
	case "brief":
		if len(turns) > 4 {
			startIdx = len(turns) - 4
		}
		maxPerTurn = 900
	case "standard":
		if len(turns) > 10 {
			startIdx = len(turns) - 10
		}
		maxPerTurn = 1800
	case "full":
		startIdx = 0
		maxPerTurn = 6000
	}

	if startIdx > 0 {
		b.WriteString(fmt.Sprintf("*(Showing last %d of %d turns; use `get_session_turns` via MCP if earlier turns are needed.)*\n\n", len(turns)-startIdx, len(turns)))
	}

	for _, t := range turns[startIdx:] {
		roleLabel := "User"
		if t.Role == "assistant" {
			roleLabel = "Assistant"
		}
		ts := ""
		if !t.Timestamp.IsZero() {
			ts = " · " + t.Timestamp.Local().Format("15:04:05")
		}
		b.WriteString(fmt.Sprintf("### Turn #%d · %s%s\n", t.Index+1, roleLabel, ts))
		if t.PlainText != "" {
			b.WriteString(clipText(t.PlainText, maxPerTurn))
			b.WriteString("\n\n")
		}
		// Summarize tool calls in this turn cleanly
		var toolNotes []string
		for _, blk := range t.Blocks {
			if blk.Type == "tool_use" {
				toolNotes = append(toolNotes, fmt.Sprintf("`%s`", blk.ToolName))
			}
		}
		if len(toolNotes) > 0 {
			b.WriteString(fmt.Sprintf("*Tools invoked in this turn*: %s\n\n", strings.Join(toolNotes, ", ")))
		}
	}

	b.WriteString("---\n")
	b.WriteString("**Next-step instruction**: Please review the context above, verify the current state of the referenced files in this workspace, and continue seamlessly from where the previous session left off.\n")
	return b.String()
}

// WriteHandoffFile saves a HANDOFF.md into the target directory (defaulting to the session's cwd).
func WriteHandoffFile(detail *parser.SessionDetail, targetDir, level string) (string, error) {
	if targetDir == "" {
		targetDir = detail.Summary.Cwd
	}
	if targetDir == "" {
		return "", fmt.Errorf("target directory is required")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	content := GenerateHandoff(detail, level)
	outPath := filepath.Join(targetDir, "HANDOFF.md")
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

// ExportMarkdown renders a clean, readable Markdown archive of the entire session.
func ExportMarkdown(detail *parser.SessionDetail) string {
	sum := detail.Summary
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n\n", sum.Title))
	b.WriteString(fmt.Sprintf("- **Project**: %s (`%s`)\n", sum.ProjectName, sum.Cwd))
	b.WriteString(fmt.Sprintf("- **Session ID**: `%s`\n", sum.SessionID))
	b.WriteString(fmt.Sprintf("- **Source**: %s\n", sum.SourceLabel))
	b.WriteString(fmt.Sprintf("- **Exported**: %s\n\n---\n\n", time.Now().Format("2006-01-02 15:04:05")))

	for _, t := range detail.Turns {
		who := "User"
		if t.Role == "assistant" {
			who = "Claude"
		}
		b.WriteString(fmt.Sprintf("## %s (Turn %d)\n\n", who, t.Index+1))
		for _, blk := range t.Blocks {
			switch blk.Type {
			case "text":
				b.WriteString(blk.Text + "\n\n")
			case "tool_use":
				b.WriteString(fmt.Sprintf("<details><summary>Tool: <code>%s</code></summary>\n\n```json\n%s\n```\n</details>\n\n", blk.ToolName, blk.ToolInput))
			case "tool_result":
				b.WriteString(fmt.Sprintf("<details><summary>Tool Result</summary>\n\n```\n%s\n```\n</details>\n\n", blk.Text))
			}
		}
	}
	return b.String()
}

func collectMeaningfulUserTurns(turns []parser.Turn) []parser.Turn {
	var out []parser.Turn
	for _, t := range turns {
		if t.Role == "user" && strings.TrimSpace(t.PlainText) != "" {
			out = append(out, t)
		}
	}
	return out
}

func clipText(s string, maxRunes int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes]) + "\n… *(truncated)*"
}
