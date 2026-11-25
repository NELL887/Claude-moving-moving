package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"claude-session-bridge/internal/config"
	"claude-session-bridge/internal/installer"
	"claude-session-bridge/internal/mcp"
	"claude-session-bridge/internal/sanitizer"
	"claude-session-bridge/internal/store"
	"claude-session-bridge/internal/web"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		runServe(nil, true)
		return
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "serve", "ui", "gui":
		runServe(args, true)
	case "mcp":
		runMCP()
	case "list", "ls":
		runList(args)
	case "search":
		runSearch(args)
	case "handoff":
		runHandoff(args)
	case "fork":
		runFork(args)
	case "install-mcp":
		runInstallMCP(args)
	case "uninstall-mcp":
		runUninstallMCP(args)
	case "doctor":
		runDoctor()
	case "-v", "--version", "version":
		fmt.Printf("Claude Session Bridge (csb) v%s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "-h", "--help", "help":
		printHelp()
	default:
		// If user passed flags directly like `csb --port 18765`, treat as serve
		if strings.HasPrefix(cmd, "-") {
			runServe(os.Args[1:], true)
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func loadStore() (*config.AppConfig, *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load config: %v\n", err)
		cfg = &config.AppConfig{Port: 18765, Language: "zh", Theme: "dark"}
	}
	st := store.New(cfg)
	return cfg, st
}

func runServe(args []string, defaultOpen bool) {
	cfg, st := loadStore()
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", cfg.Port, "Localhost HTTP port (0 for random)")
	noOpen := fs.Bool("no-open", !defaultOpen, "Do not auto-open browser window")
	_ = fs.Parse(args)

	count, _ := st.Refresh()

	// Background lightweight incremental refresh every 20 seconds
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			_, _ = st.Refresh()
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Fall back to an ephemeral localhost port if default port is busy
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to bind localhost server: %v\n", err)
			os.Exit(1)
		}
	}

	url := "http://" + ln.Addr().String()
	fmt.Printf("⚡ Claude Session Bridge v%s\n", version)
	fmt.Printf("   Indexed %d local Claude Code session(s) across %d source(s)\n", count, len(st.Sources()))
	fmt.Printf("   Local UI: %s  (localhost only · zero credential access)\n", url)
	fmt.Printf("   Press Ctrl+C to stop.\n")

	if !*noOpen {
		go func() {
			time.Sleep(250 * time.Millisecond)
			openBrowser(url)
		}()
	}

	srv := web.NewServer(st)
	if err := http.Serve(ln, srv.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "Server stopped: %v\n", err)
		os.Exit(1)
	}
}

func runMCP() {
	_, st := loadStore()
	srv := mcp.NewServer(st)
	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	limit := fs.Int("limit", 20, "Max sessions to display")
	project := fs.String("project", "", "Filter by project name or cwd substring")
	asJSON := fs.Bool("json", false, "Output as JSON")
	_ = fs.Parse(args)

	_, st := loadStore()
	_, _ = st.Refresh()
	list := st.ListSessions("", "", *project, *limit)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(list)
		return
	}
	for i, s := range list {
		fmt.Printf("%2d. [%s] %s  (%s · %d turns · %s)\n",
			i+1, short(s.SessionID, 8), s.Title, s.ProjectName, s.TurnCount, s.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
}

func runSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	limit := fs.Int("limit", 15, "Max results")
	_ = fs.Parse(args)
	query := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(query) == "" {
		fmt.Fprintln(os.Stderr, "Usage: csb search <keywords>")
		os.Exit(1)
	}
	_, st := loadStore()
	_, _ = st.Refresh()
	hits := st.Search(query, "", "", "", *limit)
	for i, h := range hits {
		fmt.Printf("%2d. [%s] %s (%s)\n    %s\n",
			i+1, short(h.Session.SessionID, 8), h.Session.Title, h.Session.ProjectName, h.Snippet)
	}
}

func runHandoff(args []string) {
	fs := flag.NewFlagSet("handoff", flag.ExitOnError)
	level := fs.String("level", "standard", "Detail level: brief, standard, full")
	writeDir := fs.String("write", "", "Write HANDOFF.md to directory instead of stdout")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: csb handoff <session-id> [--level brief|standard|full] [--write <dir>]")
		os.Exit(1)
	}
	_, st := loadStore()
	_, _ = st.Refresh()
	detail, err := st.GetSessionDetail(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if *writeDir != "" {
		p, err := sanitizer.WriteHandoffFile(detail, config.ExpandPath(*writeDir), *level)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote handoff to %s\n", p)
		return
	}
	fmt.Println(sanitizer.GenerateHandoff(detail, *level))
}

func runFork(args []string) {
	fs := flag.NewFlagSet("fork", flag.ExitOnError)
	targetCfg := fs.String("to-config", "", "Target CLAUDE_CONFIG_DIR (default ~/.claude)")
	targetCwd := fs.String("to-cwd", "", "Target project directory path")
	title := fs.String("title", "", "Custom title for forked session")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: csb fork <session-id> [--to-cwd <dir>] [--to-config <dir>] [--title <title>]")
		os.Exit(1)
	}
	_, st := loadStore()
	_, _ = st.Refresh()
	sum, ok := st.GetSessionSummary(fs.Arg(0))
	if !ok {
		fmt.Fprintf(os.Stderr, "Session not found: %s\n", fs.Arg(0))
		os.Exit(1)
	}
	res, err := sanitizer.CleanForkSession(sanitizer.CleanForkOptions{
		SourceFilePath:  sum.FilePath,
		TargetConfigDir: *targetCfg,
		TargetCwd:       *targetCwd,
		NewTitle:        *title,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Clean fork failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Clean Fork created: %s\n", res.OutputPath)
	fmt.Printf("   Sanitized: %d thinking blocks dropped, %d internal records dropped, %d metadata fields stripped\n",
		res.ThinkingBlocksDrop, res.InternalRecordsDrop, res.MetadataFieldsDrop)
	fmt.Printf("   Resume:    %s\n", res.ResumeCommand)
}

func runInstallMCP(args []string) {
	fs := flag.NewFlagSet("install-mcp", flag.ExitOnError)
	target := fs.String("target", "all", "Target ID: all, claude-cli, claude-desktop, claude-env")
	_ = fs.Parse(args)

	targets := installer.DetectTargets()
	for _, tgt := range targets {
		if *target != "all" && tgt.ID != *target {
			continue
		}
		if *target == "all" && !tgt.ParentFound && tgt.ID == "claude-desktop" {
			continue
		}
		st, bak, err := installer.InstallMCP(tgt.ID, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %s: %v\n", tgt.Name, err)
			continue
		}
		fmt.Printf("✅ Installed MCP into %s (%s)\n", st.Name, st.ConfigPath)
		if bak != "" {
			fmt.Printf("   Backup saved: %s\n", bak)
		}
	}
}

func runUninstallMCP(args []string) {
	fs := flag.NewFlagSet("uninstall-mcp", flag.ExitOnError)
	target := fs.String("target", "all", "Target ID: all, claude-cli, claude-desktop, claude-env")
	_ = fs.Parse(args)

	for _, tgt := range installer.DetectTargets() {
		if *target != "all" && tgt.ID != *target {
			continue
		}
		if !tgt.Installed {
			continue
		}
		st, bak, err := installer.UninstallMCP(tgt.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %s: %v\n", tgt.Name, err)
			continue
		}
		fmt.Printf("🗑️  Removed MCP from %s (%s)\n", st.Name, st.ConfigPath)
		if bak != "" {
			fmt.Printf("   Backup saved: %s\n", bak)
		}
	}
}

func runDoctor() {
	_, st := loadStore()
	count, _ := st.Refresh()
	fmt.Printf("Claude Session Bridge v%s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Indexed sessions: %d\n\n", count)

	fmt.Println("Discovered Claude Code Sources:")
	for _, src := range st.Sources() {
		mark := "MISSING"
		if src.Exists {
			mark = "OK"
		}
		fmt.Printf("  [%s] %-26s %s\n", mark, src.Label, src.ProjectsDir)
	}

	fmt.Println("\nDetected MCP Integration Targets:")
	for _, tgt := range installer.DetectTargets() {
		mark := "NOT MOUNTED"
		if tgt.Installed {
			mark = "MOUNTED"
		}
		fmt.Printf("  [%-11s] %-42s %s\n", mark, tgt.Name, tgt.ConfigPath)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func printHelp() {
	fmt.Printf(`Claude Session Bridge (csb) v%s — Cross-Platform Claude Code Local Session Manager & Handoff

Usage:
  csb                              Launch local desktop Web UI & open browser (default)
  csb serve [--port 18765] [--no-open]
                                   Run local desktop Web UI server
  csb mcp                          Run stdio MCP Server (for Claude CLI / Desktop / IDE)
  csb list [--limit 20] [--project <hint>] [--json]
                                   List local Claude Code sessions across all profiles
  csb search <keywords>            Full-text search all local sessions (ZH/EN)
  csb handoff <id> [--level brief|standard|full] [--write <dir>]
                                   Generate structured handoff Markdown
  csb fork <id> [--to-cwd <dir>] [--to-config <dir>] [--title <title>]
                                   Create a sanitized Clean Fork (.jsonl) for claude --resume
  csb install-mcp [--target all|claude-cli|claude-desktop]
                                   Safely back up config and mount local MCP Server
  csb uninstall-mcp [--target all|claude-cli|claude-desktop]
                                   Remove local MCP Server entry
  csb doctor                       Check discovered sources, sessions, and MCP status
`, version)
}
