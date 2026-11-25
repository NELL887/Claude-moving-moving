package parser

import "time"

// SessionSummary is the lightweight indexed metadata for one Claude Code .jsonl session.
type SessionSummary struct {
	ID             string    `json:"id"`             // Unique composite key (sourceId + encodedProject + sessionId)
	SessionID      string    `json:"sessionId"`      // Original Claude session UUID
	SourceID       string    `json:"sourceId"`       // Which config source it came from
	SourceLabel    string    `json:"sourceLabel"`    // Human-readable source label
	FilePath       string    `json:"filePath"`       // Absolute path to .jsonl
	EncodedProject string    `json:"encodedProject"` // Folder name under projects/
	ProjectKey     string    `json:"projectKey"`     // Logical cross-OS project key
	ProjectName    string    `json:"projectName"`    // Friendly project name
	GitRemote      string    `json:"gitRemote,omitempty"`
	Cwd            string    `json:"cwd"`
	GitBranch      string    `json:"gitBranch,omitempty"`
	Title          string    `json:"title"`
	FirstPrompt    string    `json:"firstPrompt"`
	LastPrompt     string    `json:"lastPrompt"`
	Entrypoint     string    `json:"entrypoint,omitempty"` // cli, desktop, ide, etc.
	Version        string    `json:"version,omitempty"`
	Model          string    `json:"model,omitempty"`
	TurnCount      int       `json:"turnCount"`
	UserCount      int       `json:"userCount"`
	AssistantCount int       `json:"assistantCount"`
	ToolUseCount   int       `json:"toolUseCount"`
	HasThinking    bool      `json:"hasThinking"`
	SubagentCount  int       `json:"subagentCount"`
	TouchedFiles   []string  `json:"touchedFiles,omitempty"`
	ToolsUsed      []string  `json:"toolsUsed,omitempty"`
	FileSize       int64     `json:"fileSize"`
	EstimatedToks  int       `json:"estimatedTokens"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	ModTime        time.Time `json:"modTime"`
}

// ContentBlock represents one normalized block inside a turn.
type ContentBlock struct {
	Type        string `json:"type"` // "text", "thinking", "tool_use", "tool_result"
	Text        string `json:"text,omitempty"`
	ToolName    string `json:"toolName,omitempty"`
	ToolID      string `json:"toolId,omitempty"`
	ToolInput   string `json:"toolInput,omitempty"`
	IsError     bool   `json:"isError,omitempty"`
	HasSig      bool   `json:"hasSignature,omitempty"`
	TruncatedTo int    `json:"truncatedTo,omitempty"`
}

// Turn represents one conversational turn (user or assistant) in a session.
type Turn struct {
	Index      int            `json:"index"`
	UUID       string         `json:"uuid,omitempty"`
	ParentUUID string         `json:"parentUuid,omitempty"`
	Role       string         `json:"role"` // "user" or "assistant"
	Timestamp  time.Time      `json:"timestamp"`
	Model      string         `json:"model,omitempty"`
	Cwd        string         `json:"cwd,omitempty"`
	GitBranch  string         `json:"gitBranch,omitempty"`
	Blocks     []ContentBlock `json:"blocks"`
	PlainText  string         `json:"plainText"`
}

// SessionDetail contains full parsed turns for reading, handoff, or export.
type SessionDetail struct {
	Summary SessionSummary `json:"summary"`
	Turns   []Turn         `json:"turns"`
}

// ProjectGroup aggregates sessions belonging to the same logical project across accounts/sources.
type ProjectGroup struct {
	ProjectKey   string    `json:"projectKey"`
	ProjectName  string    `json:"projectName"`
	GitRemote    string    `json:"gitRemote,omitempty"`
	Cwds         []string  `json:"cwds"`
	Sources      []string  `json:"sources"`
	SessionCount int       `json:"sessionCount"`
	LastUpdated  time.Time `json:"lastUpdated"`
}

// SearchResult wraps a matching session with score and snippet.
type SearchResult struct {
	Session SessionSummary `json:"session"`
	Score   float64        `json:"score"`
	Snippet string         `json:"snippet"`
}
