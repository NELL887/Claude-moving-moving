package sanitizer

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"claude-session-bridge/internal/config"
	"claude-session-bridge/internal/parser"
	"claude-session-bridge/internal/pathmap"
)

// BundleManifest is stored inside a portable .csb-bundle.zip for macOS <-> Windows transfer.
type BundleManifest struct {
	FormatVersion string    `json:"formatVersion"`
	ExportedAt    time.Time `json:"exportedAt"`
	SourceOS      string    `json:"sourceOs"`
	SessionID     string    `json:"sessionId"`
	Title         string    `json:"title"`
	ProjectName   string    `json:"projectName"`
	OriginalCwd   string    `json:"originalCwd"`
	GitRemote     string    `json:"gitRemote,omitempty"`
	GitBranch     string    `json:"gitBranch,omitempty"`
	TurnCount     int       `json:"turnCount"`
}

// ExportBundleBytes creates an in-memory .csb-bundle.zip containing:
//   - manifest.json
//   - session.sanitized.jsonl (thinking signatures & account metadata stripped)
//   - HANDOFF.md
func ExportBundleBytes(detail *parser.SessionDetail) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := BundleManifest{
		FormatVersion: "1.0",
		ExportedAt:    time.Now().UTC(),
		SourceOS:      runtime.GOOS,
		SessionID:     detail.Summary.SessionID,
		Title:         detail.Summary.Title,
		ProjectName:   detail.Summary.ProjectName,
		OriginalCwd:   detail.Summary.Cwd,
		GitRemote:     detail.Summary.GitRemote,
		GitBranch:     detail.Summary.GitBranch,
		TurnCount:     detail.Summary.TurnCount,
	}

	mBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := addZipFile(zw, "manifest.json", mBytes); err != nil {
		return nil, err
	}

	var sanitizedBuf bytes.Buffer
	if _, _, err := SanitizeJSONLStream(detail.Summary.FilePath, &sanitizedBuf, detail.Summary.SessionID, "", detail.Summary.Title, 16000); err != nil {
		return nil, err
	}
	if err := addZipFile(zw, "session.sanitized.jsonl", sanitizedBuf.Bytes()); err != nil {
		return nil, err
	}

	handoff := GenerateHandoff(detail, "standard")
	if err := addZipFile(zw, "HANDOFF.md", []byte(handoff)); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ImportBundleBytes imports a .csb-bundle.zip onto the current OS (macOS or Windows),
// remapping its cwd and writing a clean, ready-to-resume .jsonl file.
func ImportBundleBytes(zipBytes []byte, targetConfigDir, targetCwd string) (*CleanForkResult, *BundleManifest, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid bundle zip: %w", err)
	}

	var (
		manifest     BundleManifest
		jsonlContent []byte
	)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		data, rerr := io.ReadAll(rc)
		_ = rc.Close()
		if rerr != nil {
			return nil, nil, rerr
		}
		switch f.Name {
		case "manifest.json":
			_ = json.Unmarshal(data, &manifest)
		case "session.sanitized.jsonl":
			jsonlContent = data
		}
	}
	if len(jsonlContent) == 0 {
		return nil, nil, fmt.Errorf("bundle missing session.sanitized.jsonl")
	}

	// Write to temp file and run through CleanForkSession so UUID & cwd are rewritten for target OS
	tmpFile, err := os.CreateTemp("", "csb-import-*.jsonl")
	if err != nil {
		return nil, nil, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.Write(jsonlContent); err != nil {
		_ = tmpFile.Close()
		return nil, nil, err
	}
	_ = tmpFile.Close()

	if strings.TrimSpace(targetCwd) == "" {
		targetCwd = manifest.OriginalCwd
	}
	if strings.TrimSpace(targetConfigDir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			targetConfigDir = filepath.Join(home, ".claude")
		}
	} else {
		targetConfigDir = config.ExpandPath(targetConfigDir)
	}

	title := manifest.Title
	if title != "" && !strings.Contains(title, "[Imported]") {
		title = title + " [Imported]"
	}
	res, err := CleanForkSession(CleanForkOptions{
		SourceFilePath:  tmpPath,
		TargetConfigDir: targetConfigDir,
		TargetCwd:       targetCwd,
		NewTitle:        title,
	})
	if err != nil {
		return nil, nil, err
	}
	_ = pathmap.EncodeProjectPath(targetCwd)
	return res, &manifest, nil
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
