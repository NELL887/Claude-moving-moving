package sanitizer

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"claude-session-bridge/internal/config"
	"claude-session-bridge/internal/pathmap"
)

// CleanForkOptions configures how a session .jsonl is sanitized and cloned for cross-account reuse.
type CleanForkOptions struct {
	SourceFilePath    string `json:"sourceFilePath"`
	TargetConfigDir   string `json:"targetConfigDir"`   // e.g. ~/.claude or ~/.claude-work (empty = default)
	TargetCwd         string `json:"targetCwd"`         // Target project directory on current machine
	NewTitle          string `json:"newTitle"`          // Optional custom title for the forked session
	MaxToolOutputChar int    `json:"maxToolOutputChar"` // Optional trim for huge tool_result blobs (0 = default 16000)
}

// CleanForkResult reports the newly written sanitized session and statistics.
type CleanForkResult struct {
	NewSessionID        string `json:"newSessionId"`
	OutputPath          string `json:"outputPath"`
	TargetCwd           string `json:"targetCwd"`
	EncodedProject      string `json:"encodedProject"`
	ResumeCommand       string `json:"resumeCommand"`
	LinesRead           int    `json:"linesRead"`
	LinesWritten        int    `json:"linesWritten"`
	ThinkingBlocksDrop  int    `json:"thinkingBlocksDropped"`
	InternalRecordsDrop int    `json:"internalRecordsDropped"`
	MetadataFieldsDrop  int    `json:"metadataFieldsDropped"`
}

// Internal Claude Code record types tied to server/request telemetry or local file snapshots
// that should NOT be copied across accounts.
var dropRecordTypes = map[string]bool{
	"api-request":           true,
	"api-request-blob":      true,
	"api-request-shape":     true,
	"atis-latch":            true,
	"data-flow-records":     true,
	"file-history-snapshot": true,
	"file-history-delta":    true,
	"queue-operation":       true,
}

// Per-account / server-request fields removed from user/assistant records during Clean Fork.
var dropRecordFields = []string{
	"requestId",
	"requestRef",
	"wireIngestContext",
	"wireToolInputs",
	"serverClassifierRequest",
	"serverClassifierContext",
	"turnAttributionKey",
	"attributionMcpServer",
	"attributionMcpTool",
	"promptCacheRequested",
	"thinkingDisplay",
	"thinkingDurationMs",
	"toolInputStreamMs",
	"dataFlowOn",
}

// CleanForkSession reads a Claude Code .jsonl transcript, strips thinking signatures & account-bound
// metadata, rewrites sessionId & cwd, and writes a clean, standalone session file ready for `claude --resume`.
func CleanForkSession(opts CleanForkOptions) (*CleanForkResult, error) {
	if opts.SourceFilePath == "" {
		return nil, fmt.Errorf("sourceFilePath is required")
	}
	targetConfigDir := strings.TrimSpace(opts.TargetConfigDir)
	if targetConfigDir == "" {
		if env := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); env != "" {
			targetConfigDir = config.ExpandPath(env)
		} else if home, err := os.UserHomeDir(); err == nil {
			targetConfigDir = filepath.Join(home, ".claude")
		}
	} else {
		targetConfigDir = config.ExpandPath(targetConfigDir)
	}
	if targetConfigDir == "" {
		return nil, fmt.Errorf("unable to resolve targetConfigDir")
	}

	targetCwd := strings.TrimSpace(opts.TargetCwd)
	if targetCwd != "" {
		targetCwd = config.ExpandPath(targetCwd)
	}

	// Sanitize into buffer first so we can discover fallback cwd if TargetCwd wasn't supplied
	var buf bytes.Buffer
	newUUID := NewUUIDv4()
	stats, discoveredCwd, err := SanitizeJSONLStream(opts.SourceFilePath, &buf, newUUID, targetCwd, opts.NewTitle, opts.MaxToolOutputChar)
	if err != nil {
		return nil, err
	}
	if targetCwd == "" {
		targetCwd = discoveredCwd
	}
	if targetCwd == "" {
		targetCwd, _ = os.Getwd()
	}

	encodedProj := pathmap.EncodeProjectPath(targetCwd)
	outDir := filepath.Join(targetConfigDir, "projects", encodedProj)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create target project dir: %w", err)
	}
	outPath := filepath.Join(outDir, newUUID+".jsonl")
	if err := os.WriteFile(outPath, buf.Bytes(), 0o600); err != nil {
		return nil, fmt.Errorf("write forked session: %w", err)
	}

	stats.NewSessionID = newUUID
	stats.OutputPath = outPath
	stats.TargetCwd = targetCwd
	stats.EncodedProject = encodedProj
	stats.ResumeCommand = fmt.Sprintf("cd %q && claude --resume %s", targetCwd, newUUID)
	return stats, nil
}

// SanitizeJSONLStream streams and sanitizes a .jsonl file into dst.
// It is also reused by the portable .csb-bundle exporter so exported bundles never contain signatures or account metadata.
func SanitizeJSONLStream(sourceFilePath string, dst io.Writer, newSessionID, targetCwd, newTitle string, maxToolOutput int) (*CleanForkResult, string, error) {
	if maxToolOutput <= 0 {
		maxToolOutput = 16000
	}
	f, err := os.Open(sourceFilePath)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	res := &CleanForkResult{NewSessionID: newSessionID}
	discoveredCwd := ""
	uuidMap := map[string]string{}
	mapUUID := func(old string) string {
		if old == "" {
			return ""
		}
		if mapped, ok := uuidMap[old]; ok {
			return mapped
		}
		fresh := NewUUIDv4()
		uuidMap[old] = fresh
		return fresh
	}

	reader := bufio.NewReaderSize(f, 256*1024)
	writer := bufio.NewWriter(dst)
	defer writer.Flush()

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				res.LinesRead++
				var rec map[string]any
				if jerr := json.Unmarshal(trimmed, &rec); jerr == nil {
					if cwd, ok := rec["cwd"].(string); ok && cwd != "" {
						discoveredCwd = cwd
					}
					recType, _ := rec["type"].(string)
					if dropRecordTypes[recType] {
						res.InternalRecordsDrop++
						goto nextLine
					}
					if isSide, ok := rec["isSidechain"].(bool); ok && isSide {
						res.InternalRecordsDrop++
						goto nextLine
					}
					// Only preserve safe conversational & metadata records
					if recType != "user" && recType != "assistant" && recType != "custom-title" && recType != "last-prompt" && recType != "tools-used" {
						res.InternalRecordsDrop++
						goto nextLine
					}

					// Strip account/request metadata fields
					for _, field := range dropRecordFields {
						if _, exists := rec[field]; exists {
							delete(rec, field)
							res.MetadataFieldsDrop++
						}
					}

					// Rewrite sessionId, uuid, parentUuid, cwd
					rec["sessionId"] = newSessionID
					if oldU, ok := rec["uuid"].(string); ok && oldU != "" {
						rec["uuid"] = mapUUID(oldU)
					}
					if oldP, ok := rec["parentUuid"].(string); ok && oldP != "" {
						rec["parentUuid"] = mapUUID(oldP)
					}
					if oldSrc, ok := rec["sourceToolAssistantUUID"].(string); ok && oldSrc != "" {
						rec["sourceToolAssistantUUID"] = mapUUID(oldSrc)
					}
					if targetCwd != "" {
						if _, hasCwd := rec["cwd"]; hasCwd {
							rec["cwd"] = targetCwd
						}
					}

					// Sanitize message blocks (strip thinking + signature, cap huge tool_result)
					if msg, ok := rec["message"].(map[string]any); ok {
						// Drop per-request API message id / usage / container hints that could confuse replay
						delete(msg, "id")
						if contentArr, ok := msg["content"].([]any); ok {
							cleaned := make([]any, 0, len(contentArr))
							for _, item := range contentArr {
								blk, ok := item.(map[string]any)
								if !ok {
									cleaned = append(cleaned, item)
									continue
								}
								bt, _ := blk["type"].(string)
								if bt == "thinking" || bt == "redacted_thinking" {
									res.ThinkingBlocksDrop++
									continue
								}
								if _, hasSig := blk["signature"]; hasSig {
									delete(blk, "signature")
									res.MetadataFieldsDrop++
								}
								if bt == "tool_result" {
									trimToolResultBlock(blk, maxToolOutput)
								}
								cleaned = append(cleaned, blk)
							}
							if len(cleaned) == 0 {
								// If an assistant record ONLY had a thinking block, skip writing empty message
								res.InternalRecordsDrop++
								goto nextLine
							}
							msg["content"] = cleaned
						}
					}

					outBytes, merr := json.Marshal(rec)
					if merr == nil {
						_, _ = writer.Write(outBytes)
						_ = writer.WriteByte('\n')
						res.LinesWritten++
					}
				}
			}
		}
	nextLine:
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, discoveredCwd, err
		}
	}

	// Optionally append custom title record for the new fork
	if strings.TrimSpace(newTitle) != "" {
		titleRec := map[string]any{
			"type":        "custom-title",
			"sessionId":   newSessionID,
			"customTitle": strings.TrimSpace(newTitle),
			"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		}
		if b, err := json.Marshal(titleRec); err == nil {
			_, _ = writer.Write(b)
			_ = writer.WriteByte('\n')
			res.LinesWritten++
		}
	}

	return res, discoveredCwd, nil
}

func trimToolResultBlock(blk map[string]any, maxChars int) {
	if s, ok := blk["content"].(string); ok {
		r := []rune(s)
		if len(r) > maxChars {
			blk["content"] = string(r[:maxChars]) + "\n... [truncated by Claude Session Bridge during clean fork]"
		}
	}
}

// NewUUIDv4 generates a standard RFC 4122 version 4 UUID using crypto/rand.
func NewUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
