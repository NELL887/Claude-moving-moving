package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"claude-session-bridge/internal/config"
	"claude-session-bridge/internal/parser"
)

type indexedEntry struct {
	Summary      parser.SessionSummary `json:"summary"`
	SearchCorpus string                `json:"searchCorpus"`
	MtimeUnix    int64                 `json:"mtimeUnix"`
	FileSize     int64                 `json:"fileSize"`
}

type diskCache struct {
	Version int                      `json:"version"`
	Entries map[string]*indexedEntry `json:"entries"`
}

// Store maintains the incrementally-updated index of all Claude Code sessions across sources.
type Store struct {
	mu           sync.RWMutex
	cfg          *config.AppConfig
	sources      []config.Source
	entries      map[string]*indexedEntry // key: filePath
	byID         map[string]*indexedEntry // key: Summary.ID
	bySessionID  map[string]*indexedEntry // key: Summary.SessionID (latest wins)
	lastScanAt   time.Time
	cachePath    string
}

func New(cfg *config.AppConfig) *Store {
	cachePath := ""
	if appDir, err := config.DefaultAppDir(); err == nil {
		cachePath = filepath.Join(appDir, "index_cache.json")
	}
	s := &Store{
		cfg:         cfg,
		entries:     make(map[string]*indexedEntry),
		byID:        make(map[string]*indexedEntry),
		bySessionID: make(map[string]*indexedEntry),
		cachePath:   cachePath,
	}
	s.loadCache()
	return s
}

func (s *Store) SetConfig(cfg *config.AppConfig) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *Store) Config() *config.AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Sources() []config.Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.Source, len(s.sources))
	copy(out, s.sources)
	return out
}

func (s *Store) LastScanAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastScanAt
}

// Refresh scans all enabled Claude Code sources incrementally (re-parsing only changed .jsonl files).
func (s *Store) Refresh() (int, error) {
	s.mu.RLock()
	cfg := s.cfg
	existing := make(map[string]*indexedEntry, len(s.entries))
	for k, v := range s.entries {
		existing[k] = v
	}
	s.mu.RUnlock()

	sources := config.DiscoverSources(cfg)
	newEntries := make(map[string]*indexedEntry)
	changed := false

	for _, src := range sources {
		if !src.Enabled || !src.Exists {
			continue
		}
		projDirs, err := os.ReadDir(src.ProjectsDir)
		if err != nil {
			continue
		}
		for _, pd := range projDirs {
			if !pd.IsDir() {
				continue
			}
			encodedProject := pd.Name()
			fullProjDir := filepath.Join(src.ProjectsDir, encodedProject)
			files, err := os.ReadDir(fullProjDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				fp := filepath.Join(fullProjDir, f.Name())
				info, err := f.Info()
				if err != nil || info.Size() == 0 {
					continue
				}
				mtime := info.ModTime().UnixNano()
				if prev, ok := existing[fp]; ok && prev.MtimeUnix == mtime && prev.FileSize == info.Size() && prev.Summary.SourceID == src.ID {
					newEntries[fp] = prev
					continue
				}
				res, err := parser.ParseSessionFile(fp, src.ID, src.Label, encodedProject, false)
				if err != nil {
					continue
				}
				// Skip files that have zero user/assistant turns (pure telemetry stubs)
				if res.Summary.TurnCount == 0 {
					continue
				}
				newEntries[fp] = &indexedEntry{
					Summary:      res.Summary,
					SearchCorpus: res.SearchCorpus,
					MtimeUnix:    mtime,
					FileSize:     info.Size(),
				}
				changed = true
			}
		}
	}

	if len(newEntries) != len(existing) {
		changed = true
	}

	byID := make(map[string]*indexedEntry, len(newEntries))
	bySID := make(map[string]*indexedEntry, len(newEntries))
	for _, e := range newEntries {
		byID[e.Summary.ID] = e
		if prev, ok := bySID[e.Summary.SessionID]; !ok || e.Summary.UpdatedAt.After(prev.Summary.UpdatedAt) {
			bySID[e.Summary.SessionID] = e
		}
	}

	s.mu.Lock()
	s.sources = sources
	s.entries = newEntries
	s.byID = byID
	s.bySessionID = bySID
	s.lastScanAt = time.Now().UTC()
	s.mu.Unlock()

	if changed {
		s.saveCache()
	}
	return len(newEntries), nil
}

// ListSessions returns sessions filtered by optional sourceID / projectKey / projectHint, sorted newest first.
func (s *Store) ListSessions(sourceID, projectKey, projectHint string, limit int) []parser.SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hintLower := strings.ToLower(strings.TrimSpace(projectHint))
	out := make([]parser.SessionSummary, 0, len(s.entries))
	for _, e := range s.entries {
		sum := e.Summary
		if sourceID != "" && sum.SourceID != sourceID {
			continue
		}
		if projectKey != "" && sum.ProjectKey != projectKey {
			continue
		}
		if hintLower != "" {
			if !strings.Contains(strings.ToLower(sum.ProjectName), hintLower) &&
				!strings.Contains(strings.ToLower(sum.Cwd), hintLower) &&
				!strings.Contains(strings.ToLower(sum.GitRemote), hintLower) {
				continue
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ListProjects aggregates all sessions into logical cross-OS project groups.
func (s *Store) ListProjects(sourceID string) []parser.ProjectGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()

	groups := map[string]*parser.ProjectGroup{}
	cwdSets := map[string]map[string]struct{}{}
	srcSets := map[string]map[string]struct{}{}

	for _, e := range s.entries {
		sum := e.Summary
		if sourceID != "" && sum.SourceID != sourceID {
			continue
		}
		g, ok := groups[sum.ProjectKey]
		if !ok {
			g = &parser.ProjectGroup{
				ProjectKey:  sum.ProjectKey,
				ProjectName: sum.ProjectName,
				GitRemote:   sum.GitRemote,
			}
			groups[sum.ProjectKey] = g
			cwdSets[sum.ProjectKey] = map[string]struct{}{}
			srcSets[sum.ProjectKey] = map[string]struct{}{}
		}
		g.SessionCount++
		if sum.UpdatedAt.After(g.LastUpdated) {
			g.LastUpdated = sum.UpdatedAt
		}
		if sum.Cwd != "" {
			cwdSets[sum.ProjectKey][sum.Cwd] = struct{}{}
		}
		srcSets[sum.ProjectKey][sum.SourceID] = struct{}{}
	}

	out := make([]parser.ProjectGroup, 0, len(groups))
	for key, g := range groups {
		for c := range cwdSets[key] {
			g.Cwds = append(g.Cwds, c)
		}
		sort.Strings(g.Cwds)
		for sr := range srcSets[key] {
			g.Sources = append(g.Sources, sr)
		}
		sort.Strings(g.Sources)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastUpdated.After(out[j].LastUpdated)
	})
	return out
}

// GetSessionSummary finds a session by composite ID or raw Claude SessionID (prefix match supported).
func (s *Store) GetSessionSummary(id string) (*parser.SessionSummary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if e, ok := s.byID[id]; ok {
		cp := e.Summary
		return &cp, true
	}
	if e, ok := s.bySessionID[id]; ok {
		cp := e.Summary
		return &cp, true
	}
	// Prefix match on SessionID for CLI convenience
	if len(id) >= 6 {
		for sid, e := range s.bySessionID {
			if strings.HasPrefix(sid, id) {
				cp := e.Summary
				return &cp, true
			}
		}
	}
	return nil, false
}

// GetSessionDetail parses the full turns of a session directly from disk.
func (s *Store) GetSessionDetail(id string) (*parser.SessionDetail, error) {
	sum, ok := s.GetSessionSummary(id)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	res, err := parser.ParseSessionFile(sum.FilePath, sum.SourceID, sum.SourceLabel, sum.EncodedProject, true)
	if err != nil {
		return nil, err
	}
	return &parser.SessionDetail{
		Summary: res.Summary,
		Turns:   res.Turns,
	}, nil
}

// Search performs multi-language (Chinese + English) full-text search across all indexed sessions.
func (s *Store) Search(query, sourceID, projectKey, projectHint string, limit int) []parser.SearchResult {
	query = strings.TrimSpace(query)
	if query == "" {
		list := s.ListSessions(sourceID, projectKey, projectHint, limit)
		res := make([]parser.SearchResult, len(list))
		for i, sum := range list {
			res[i] = parser.SearchResult{Session: sum, Score: 1, Snippet: sum.FirstPrompt}
		}
		return res
	}

	terms := tokenizeQuery(query)
	queryLower := strings.ToLower(query)
	hintLower := strings.ToLower(strings.TrimSpace(projectHint))

	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []parser.SearchResult
	for _, e := range s.entries {
		sum := e.Summary
		if sourceID != "" && sum.SourceID != sourceID {
			continue
		}
		if projectKey != "" && sum.ProjectKey != projectKey {
			continue
		}
		if hintLower != "" {
			if !strings.Contains(strings.ToLower(sum.ProjectName), hintLower) &&
				!strings.Contains(strings.ToLower(sum.Cwd), hintLower) {
				continue
			}
		}

		titleLower := strings.ToLower(sum.Title)
		projLower := strings.ToLower(sum.ProjectName + " " + sum.Cwd)
		corpusLower := strings.ToLower(e.SearchCorpus)

		score := 0.0
		matchedTerms := 0

		// Exact phrase boost
		if strings.Contains(titleLower, queryLower) {
			score += 25
		}
		if strings.Contains(projLower, queryLower) {
			score += 15
		}
		if strings.Contains(corpusLower, queryLower) {
			score += 12
		}

		for _, t := range terms {
			hit := false
			if strings.Contains(titleLower, t) {
				score += 8
				hit = true
			}
			if strings.Contains(projLower, t) {
				score += 5
				hit = true
			}
			if cnt := strings.Count(corpusLower, t); cnt > 0 {
				if cnt > 10 {
					cnt = 10
				}
				score += float64(cnt) * 1.5
				hit = true
			}
			if hit {
				matchedTerms++
			}
		}

		if score == 0 || (len(terms) > 1 && matchedTerms == 0) {
			continue
		}

		// Slight recency bonus
		ageHours := time.Since(sum.UpdatedAt).Hours()
		if ageHours < 24 {
			score += 2.0
		} else if ageHours < 24*7 {
			score += 1.0
		}

		snippet := extractSnippet(e.SearchCorpus, queryLower, terms)
		results = append(results, parser.SearchResult{
			Session: sum,
			Score:   score,
			Snippet: snippet,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Session.UpdatedAt.After(results[j].Session.UpdatedAt)
		}
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func tokenizeQuery(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，' || r == ';' || r == '；'
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func extractSnippet(corpus, queryLower string, terms []string) string {
	lower := strings.ToLower(corpus)
	idx := strings.Index(lower, queryLower)
	if idx < 0 {
		for _, t := range terms {
			if pos := strings.Index(lower, t); pos >= 0 {
				idx = pos
				break
			}
		}
	}
	if idx < 0 {
		r := []rune(strings.TrimSpace(corpus))
		if len(r) > 160 {
			return string(r[:160]) + "…"
		}
		return string(r)
	}

	// Convert byte offset safely to rune slice window
	prefix := []rune(corpus[:idx])
	allRunes := []rune(corpus)
	runePos := len(prefix)
	start := runePos - 45
	if start < 0 {
		start = 0
	}
	end := runePos + 125
	if end > len(allRunes) {
		end = len(allRunes)
	}
	snip := strings.ReplaceAll(string(allRunes[start:end]), "\n", " ")
	snip = strings.Join(strings.Fields(snip), " ")
	if start > 0 {
		snip = "…" + snip
	}
	if end < len(allRunes) {
		snip = snip + "…"
	}
	return snip
}

func (s *Store) loadCache() {
	if s.cachePath == "" {
		return
	}
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		return
	}
	var dc diskCache
	if err := json.Unmarshal(data, &dc); err != nil || dc.Version != 1 {
		return
	}
	for fp, e := range dc.Entries {
		if e == nil {
			continue
		}
		s.entries[fp] = e
		s.byID[e.Summary.ID] = e
		s.bySessionID[e.Summary.SessionID] = e
	}
}

func (s *Store) saveCache() {
	if s.cachePath == "" {
		return
	}
	s.mu.RLock()
	dc := diskCache{
		Version: 1,
		Entries: make(map[string]*indexedEntry, len(s.entries)),
	}
	for k, v := range s.entries {
		dc.Entries[k] = v
	}
	s.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(s.cachePath), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(dc)
	if err != nil {
		return
	}
	tmp := s.cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, s.cachePath)
	}
}
