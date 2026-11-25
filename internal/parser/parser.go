package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"claude-session-bridge/internal/pathmap"
)

const (
	maxToolResultPreview = 2400
	maxSearchCorpusBytes = 180000
)

// ParseResult holds both the summary and searchable corpus built in one streaming pass.
type ParseResult struct {
	Summary      SessionSummary
	Turns        []Turn
	SearchCorpus string
}

// ParseSessionFile streams a Claude Code .jsonl file and extracts metadata, turns, and search text.
// If includeTurns is false, Turns is omitted to keep memory low during indexing.
func ParseSessionFile(filePath, sourceID, sourceLabel, encodedProject string, includeTurns bool) (*ParseResult, error) {
	st, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	baseSessionID := strings.TrimSuffix(filepath.Base(filePath), ".jsonl")
	sum := SessionSummary{
		SessionID:      baseSessionID,
		SourceID:       sourceID,
		SourceLabel:    sourceLabel,
		FilePath:       filePath,
		EncodedProject: encodedProject,
		FileSize:       st.Size(),
		ModTime:        st.ModTime().UTC(),
	}

	var (
		turns        []Turn
		corpus       strings.Builder
		touchedSet   = map[string]struct{}{}
		toolsSet     = map[string]struct{}{}
		customTitle  string
		agentName    string
		lastPrompt   string
		totalChars   int
		turnIdx      int
		reader       = bufio.NewReaderSize(f, 256*1024)
	)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				var raw map[string]any
				if jerr := json.Unmarshal(line, &raw); jerr == nil {
					recType := getString(raw, "type")
					if sid := getString(raw, "sessionId"); sid != "" && sum.SessionID == baseSessionID {
						sum.SessionID = sid
					}
					if cwd := getString(raw, "cwd"); cwd != "" {
						sum.Cwd = cwd
					}
					if relocated := getString(raw, "relocatedCwd"); relocated != "" {
						sum.Cwd = relocated
					}
					if gb := getString(raw, "gitBranch"); gb != "" {
						sum.GitBranch = gb
					}
					if ep := getString(raw, "entrypoint"); ep != "" && sum.Entrypoint == "" {
						sum.Entrypoint = ep
					}
					if ver := getString(raw, "version"); ver != "" && sum.Version == "" {
						sum.Version = ver
					}

					ts := parseTimestamp(getString(raw, "timestamp"))
					if !ts.IsZero() {
						if sum.CreatedAt.IsZero() || ts.Before(sum.CreatedAt) {
							sum.CreatedAt = ts
						}
						if ts.After(sum.UpdatedAt) {
							sum.UpdatedAt = ts
						}
					}

					switch recType {
					case "custom-title":
						if ct := getString(raw, "customTitle"); ct != "" {
							customTitle = ct
						}
					case "agent-name":
						if an := getString(raw, "agentName"); an != "" {
							agentName = an
						}
					case "last-prompt":
						if lp := getString(raw, "lastPrompt"); lp != "" {
							lastPrompt = lp
						}
					case "tools-used":
						if arr, ok := raw["tools"].([]any); ok {
							for _, t := range arr {
								if s, ok := t.(string); ok && s != "" {
									toolsSet[s] = struct{}{}
								}
							}
						}
					case "user", "assistant":
						// Skip sidechain synthetic records if marked, or keep them?
						// Sidechain messages belong to subagents; keep main thread clean by skipping sidechains if flag set.
						if isSide, ok := raw["isSidechain"].(bool); ok && isSide {
							break
						}
						msg, ok := raw["message"].(map[string]any)
						if !ok {
							break
						}
						role := getString(msg, "role")
						if role == "" {
							role = recType
						}
						model := getString(msg, "model")
						if model == "" {
							model = getString(raw, "requestedModel")
						}
						if model != "" && role == "assistant" {
							sum.Model = model
						}

						blocks, plain, hasThink, toolCount := extractMessageContent(msg["content"], toolsSet, touchedSet, includeTurns)
						if hasThink {
							sum.HasThinking = true
						}
						sum.ToolUseCount += toolCount

						// Skip completely empty tool-only carrier user messages when counting prompts
						cleanedPlain := strings.TrimSpace(plain)
						if len(blocks) == 0 && cleanedPlain == "" {
							break
						}

						if role == "user" {
							sum.UserCount++
							if cleanedPlain != "" && !isSyntheticPrompt(cleanedPlain) {
								if sum.FirstPrompt == "" {
									sum.FirstPrompt = truncateRunes(cleanedPlain, 180)
								}
								sum.LastPrompt = truncateRunes(cleanedPlain, 180)
							}
						} else if role == "assistant" {
							sum.AssistantCount++
						}
						sum.TurnCount++
						totalChars += len(cleanedPlain)

						if corpus.Len() < maxSearchCorpusBytes && cleanedPlain != "" {
							corpus.WriteString(cleanedPlain)
							corpus.WriteByte('\n')
						}

						if includeTurns {
							turns = append(turns, Turn{
								Index:      turnIdx,
								UUID:       getString(raw, "uuid"),
								ParentUUID: getString(raw, "parentUuid"),
								Role:       role,
								Timestamp:  ts,
								Model:      model,
								Cwd:        getString(raw, "cwd"),
								GitBranch:  getString(raw, "gitBranch"),
								Blocks:     blocks,
								PlainText:  cleanedPlain,
							})
							turnIdx++
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}

	if sum.CreatedAt.IsZero() {
		sum.CreatedAt = sum.ModTime
	}
	if sum.UpdatedAt.IsZero() {
		sum.UpdatedAt = sum.ModTime
	}
	if lastPrompt != "" && sum.LastPrompt == "" {
		sum.LastPrompt = truncateRunes(strings.TrimSpace(lastPrompt), 180)
	}

	// Pick best display title
	switch {
	case customTitle != "":
		sum.Title = customTitle
	case sum.FirstPrompt != "":
		sum.Title = firstLine(sum.FirstPrompt, 90)
	case lastPrompt != "":
		sum.Title = firstLine(lastPrompt, 90)
	case agentName != "":
		sum.Title = agentName
	default:
		sum.Title = "Session " + shortID(sum.SessionID)
	}

	sum.ID = sourceID + "::" + encodedProject + "::" + sum.SessionID
	sum.ProjectKey, sum.ProjectName, sum.GitRemote = pathmap.LogicalProjectKey(sum.Cwd, encodedProject)
	sum.EstimatedToks = estimateTokens(totalChars, st.Size())

	// Check sibling <session-id>/subagents/*.jsonl
	subDir := filepath.Join(filepath.Dir(filePath), baseSessionID, "subagents")
	if entries, err := os.ReadDir(subDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				sum.SubagentCount++
			}
		}
	}

	if len(touchedSet) > 0 {
		sum.TouchedFiles = make([]string, 0, len(touchedSet))
		for p := range touchedSet {
			sum.TouchedFiles = append(sum.TouchedFiles, p)
		}
		sort.Strings(sum.TouchedFiles)
		if len(sum.TouchedFiles) > 50 {
			sum.TouchedFiles = sum.TouchedFiles[:50]
		}
	}
	if len(toolsSet) > 0 {
		sum.ToolsUsed = make([]string, 0, len(toolsSet))
		for t := range toolsSet {
			sum.ToolsUsed = append(sum.ToolsUsed, t)
		}
		sort.Strings(sum.ToolsUsed)
	}

	// Include metadata in search corpus for easy search by project, title, branch, touched files
	var metaPrefix strings.Builder
	metaPrefix.WriteString(sum.Title)
	metaPrefix.WriteByte('\n')
	metaPrefix.WriteString(sum.ProjectName)
	metaPrefix.WriteByte('\n')
	metaPrefix.WriteString(sum.Cwd)
	metaPrefix.WriteByte('\n')
	metaPrefix.WriteString(sum.GitBranch)
	metaPrefix.WriteByte('\n')
	for _, f := range sum.TouchedFiles {
		metaPrefix.WriteString(f)
		metaPrefix.WriteByte(' ')
	}
	metaPrefix.WriteByte('\n')
	metaPrefix.WriteString(corpus.String())

	return &ParseResult{
		Summary:      sum,
		Turns:        turns,
		SearchCorpus: metaPrefix.String(),
	}, nil
}

func extractMessageContent(content any, toolsSet, touchedSet map[string]struct{}, includeBlocks bool) ([]ContentBlock, string, bool, int) {
	var (
		blocks    []ContentBlock
		plain     strings.Builder
		hasThink  bool
		toolCount int
	)

	switch v := content.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text != "" {
			plain.WriteString(text)
			if includeBlocks {
				blocks = append(blocks, ContentBlock{Type: "text", Text: text})
			}
		}
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			bt := getString(m, "type")
			switch bt {
			case "text":
				t := strings.TrimSpace(getString(m, "text"))
				if t != "" {
					if plain.Len() > 0 {
						plain.WriteString("\n\n")
					}
					plain.WriteString(t)
					if includeBlocks {
						blocks = append(blocks, ContentBlock{Type: "text", Text: t})
					}
				}
			case "thinking", "redacted_thinking":
				hasThink = true
				t := strings.TrimSpace(getString(m, "thinking"))
				hasSig := getString(m, "signature") != ""
				if includeBlocks && (t != "" || bt == "redacted_thinking") {
					if t == "" {
						t = "[redacted thinking block]"
					}
					blocks = append(blocks, ContentBlock{
						Type:   "thinking",
						Text:   truncateRunes(t, 4000),
						HasSig: hasSig,
					})
				}
			case "tool_use":
				toolCount++
				name := getString(m, "name")
				if name != "" {
					toolsSet[name] = struct{}{}
				}
				inputMap, _ := m["input"].(map[string]any)
				collectTouchedFiles(name, inputMap, touchedSet)
				if includeBlocks {
					inputStr := ""
					if inputMap != nil {
						if b, err := json.MarshalIndent(inputMap, "", "  "); err == nil {
							inputStr = truncateRunes(string(b), 2000)
						}
					}
					blocks = append(blocks, ContentBlock{
						Type:      "tool_use",
						ToolName:  name,
						ToolID:    getString(m, "id"),
						ToolInput: inputStr,
					})
				}
			case "tool_result":
				isErr, _ := m["is_error"].(bool)
				resText := extractToolResultText(m["content"])
				if includeBlocks {
					trunc := 0
					if len([]rune(resText)) > maxToolResultPreview {
						trunc = maxToolResultPreview
						resText = truncateRunes(resText, maxToolResultPreview)
					}
					blocks = append(blocks, ContentBlock{
						Type:        "tool_result",
						ToolID:      getString(m, "tool_use_id"),
						Text:        resText,
						IsError:     isErr,
						TruncatedTo: trunc,
					})
				}
			}
		}
	}

	return blocks, plain.String(), hasThink, toolCount
}

func extractToolResultText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t := getString(m, "text"); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if v == nil {
			return ""
		}
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func collectTouchedFiles(toolName string, input map[string]any, touched map[string]struct{}) {
	if input == nil {
		return
	}
	for _, key := range []string{"file_path", "path", "notebook_path", "target_file", "filename"} {
		if val, ok := input[key].(string); ok {
			val = strings.TrimSpace(val)
			if val != "" && len(val) < 300 && !strings.ContainsAny(val, "\n\r") {
				touched[val] = struct{}{}
			}
		}
	}
	_ = toolName
}

func isSyntheticPrompt(s string) bool {
	return strings.HasPrefix(s, "<command-name>") ||
		strings.HasPrefix(s, "<local-command-") ||
		strings.HasPrefix(s, "[Request interrupted")
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func firstLine(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return truncateRunes(s, maxRunes)
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func estimateTokens(plainChars int, fileSize int64) int {
	// Rough heuristic: ~3.5 chars per token for mixed CJK/English text
	if plainChars > 0 {
		return plainChars/3 + 1
	}
	return int(fileSize/16) + 1
}
