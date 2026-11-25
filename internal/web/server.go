package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"claude-session-bridge/internal/config"
	"claude-session-bridge/internal/desktop"
	"claude-session-bridge/internal/installer"
	"claude-session-bridge/internal/sanitizer"
	"claude-session-bridge/internal/store"
)

//go:embed static/*
var staticFS embed.FS

// Server serves the embedded 3-pane desktop UI and localhost-only JSON API.
type Server struct {
	store *store.Store
	mux   *http.ServeMux
}

func NewServer(st *store.Store) *Server {
	s := &Server{
		store: st,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strict localhost origin check on mutating API requests to prevent cross-site CSRF.
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			if origin != "" && !strings.HasPrefix(origin, "http://127.0.0.1") && !strings.HasPrefix(origin, "http://localhost") {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden origin"})
				return
			}
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	sub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(sub))

	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/refresh", s.handleRefresh)
	s.mux.HandleFunc("/api/sources", s.handleSources)
	s.mux.HandleFunc("/api/projects", s.handleProjects)
	s.mux.HandleFunc("/api/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/session", s.handleSessionDetail)
	s.mux.HandleFunc("/api/handoff", s.handleHandoff)
	s.mux.HandleFunc("/api/handoff/write", s.handleHandoffWrite)
	s.mux.HandleFunc("/api/clean-fork", s.handleCleanFork)
	s.mux.HandleFunc("/api/export/markdown", s.handleExportMarkdown)
	s.mux.HandleFunc("/api/export/bundle", s.handleExportBundle)
	s.mux.HandleFunc("/api/import/bundle", s.handleImportBundle)
	s.mux.HandleFunc("/api/mcp/targets", s.handleMCPTargets)
	s.mux.HandleFunc("/api/mcp/install", s.handleMCPInstall)
	s.mux.HandleFunc("/api/mcp/uninstall", s.handleMCPUninstall)
	s.mux.HandleFunc("/api/desktop/workspaces", s.handleDesktopWorkspaces)
	s.mux.HandleFunc("/api/desktop/push", s.handleDesktopPush)
	s.mux.Handle("/", fileServer)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	exe, _ := os.Executable()
	sessions := s.store.ListSessions("", "", "", 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"version":      "1.0.0",
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"executable":   exe,
		"sessionCount": len(sessions),
		"lastScanAt":   s.store.LastScanAt(),
	})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.Refresh()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"sessionCount": count,
		"lastScanAt":   s.store.LastScanAt(),
	})
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"sources": s.store.Sources(),
		})
	case http.MethodPost:
		var req struct {
			Label     string `json:"label"`
			ConfigDir string `json:"configDir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		dir := config.ExpandPath(req.ConfigDir)
		if dir == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "configDir is required"})
			return
		}
		cfg := s.store.Config()
		label := strings.TrimSpace(req.Label)
		if label == "" {
			label = filepath.Base(dir)
		}
		cfg.CustomSources = append(cfg.CustomSources, config.Source{
			Label:     label,
			ConfigDir: dir,
			Kind:      "custom",
			Enabled:   true,
		})
		if err := config.Save(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.store.SetConfig(cfg)
		count, _ := s.store.Refresh()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"sources":      s.store.Sources(),
			"sessionCount": count,
		})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		cfg := s.store.Config()
		filtered := make([]config.Source, 0, len(cfg.CustomSources))
		for _, cs := range cfg.CustomSources {
			if cs.ID != id && cs.ConfigDir != id {
				filtered = append(filtered, cs)
			}
		}
		cfg.CustomSources = filtered
		_ = config.Save(cfg)
		s.store.SetConfig(cfg)
		count, _ := s.store.Refresh()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"sources":      s.store.Sources(),
			"sessionCount": count,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	sourceID := r.URL.Query().Get("source")
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": s.store.ListProjects(sourceID),
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	sourceID := r.URL.Query().Get("source")
	projectKey := r.URL.Query().Get("project")
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if q != "" {
		hits := s.store.Search(q, sourceID, projectKey, "", limit)
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":    "search",
			"results": hits,
		})
		return
	}
	list := s.store.ListSessions(sourceID, projectKey, "", limit)
	results := make([]map[string]any, len(list))
	for i, item := range list {
		results[i] = map[string]any{
			"session": item,
			"score":   1,
			"snippet": item.FirstPrompt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":    "list",
		"results": results,
	})
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	detail, err := s.store.GetSessionDetail(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	level := r.URL.Query().Get("level")
	if level == "" {
		level = "standard"
	}
	detail, err := s.store.GetSessionDetail(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	content := sanitizer.GenerateHandoff(detail, level)
	mcpPrompt := fmt.Sprintf("请使用 claude-session-bridge MCP 工具调用 get_session_handoff(session_id=%q, detail_level=%q)，读取该历史会话上下文并核对当前工作区文件，然后无缝继续之前的任务。", detail.Summary.SessionID, level)
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": detail.Summary.SessionID,
		"level":     level,
		"markdown":  content,
		"mcpPrompt": mcpPrompt,
	})
}

func (s *Server) handleHandoffWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID        string `json:"id"`
		TargetDir string `json:"targetDir"`
		Level     string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	detail, err := s.store.GetSessionDetail(req.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	outPath, err := sanitizer.WriteHandoffFile(detail, config.ExpandPath(req.TargetDir), req.Level)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": outPath})
}

func (s *Server) handleCleanFork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID              string `json:"id"`
		TargetConfigDir string `json:"targetConfigDir"`
		TargetCwd       string `json:"targetCwd"`
		NewTitle        string `json:"newTitle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	sum, ok := s.store.GetSessionSummary(req.ID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	res, err := sanitizer.CleanForkSession(sanitizer.CleanForkOptions{
		SourceFilePath:  sum.FilePath,
		TargetConfigDir: req.TargetConfigDir,
		TargetCwd:       req.TargetCwd,
		NewTitle:        req.NewTitle,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_, _ = s.store.Refresh()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleExportMarkdown(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	detail, err := s.store.GetSessionDetail(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	md := sanitizer.ExportMarkdown(detail)
	fname := fmt.Sprintf("claude-session-%s.md", shortSafe(detail.Summary.SessionID))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fname))
	_, _ = w.Write([]byte(md))
}

func (s *Server) handleExportBundle(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	detail, err := s.store.GetSessionDetail(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	data, err := sanitizer.ExportBundleBytes(detail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fname := fmt.Sprintf("session-%s.csb-bundle.zip", shortSafe(detail.Summary.SessionID))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fname))
	_, _ = w.Write(data)
}

func (s *Server) handleImportBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form: " + err.Error()})
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bundle file"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	targetConfigDir := r.FormValue("targetConfigDir")
	targetCwd := r.FormValue("targetCwd")
	res, manifest, err := sanitizer.ImportBundleBytes(data, targetConfigDir, targetCwd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_, _ = s.store.Refresh()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"result":   res,
		"manifest": manifest,
	})
}

func (s *Server) handleMCPTargets(w http.ResponseWriter, r *http.Request) {
	exe, _ := os.Executable()
	writeJSON(w, http.StatusOK, map[string]any{
		"executable": exe,
		"targets":    installer.DetectTargets(),
	})
}

func (s *Server) handleMCPInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TargetID string `json:"targetId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	st, backup, err := installer.InstallMCP(req.TargetID, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"target":     st,
		"backupPath": backup,
		"targets":    installer.DetectTargets(),
	})
}

func (s *Server) handleMCPUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TargetID string `json:"targetId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	st, backup, err := installer.UninstallMCP(req.TargetID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"target":     st,
		"backupPath": backup,
		"targets":    installer.DetectTargets(),
	})
}

func (s *Server) handleDesktopWorkspaces(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"root":       desktop.SessionsRootDir(),
		"workspaces": desktop.DetectWorkspaces(),
	})
}

func (s *Server) handleDesktopPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID           string `json:"id"`
		WorkspaceDir string `json:"workspaceDir"`
		TargetCwd    string `json:"targetCwd"`
		Title        string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	sum, ok := s.store.GetSessionSummary(req.ID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = sum.Title
	}
	cwd := strings.TrimSpace(req.TargetCwd)
	if cwd == "" {
		cwd = sum.Cwd
	}
	res, err := desktop.PushToDesktopSidebar(desktop.PushOptions{
		SourceFilePath: sum.FilePath,
		WorkspaceDir:   req.WorkspaceDir,
		TargetCwd:      cwd,
		Title:          title,
		Model:          sum.Model,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_, _ = s.store.Refresh()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": res,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func shortSafe(s string) string {
	if len(s) > 12 {
		s = s[:12]
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "export"
	}
	return b.String()
}
