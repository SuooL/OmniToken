// omnitoken is a single binary with two roles:
//
//	omnitoken serve  — central server: ingest + storage + web dashboard,
//	                   plus built-in collectors (local scan & SSH pull)
//	omnitoken agent  — push-mode collector for machines that report over
//	                   HTTP (direct, mesh VPN, or via a relay peer)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/agent"
	"github.com/suool/omnitoken/internal/server"
	"github.com/suool/omnitoken/internal/statusline"
)

// version is stamped at build time via -ldflags "-X main.version=...".
// A plain `go build` or `go install` leaves it as "dev", which is honest:
// such a binary genuinely has no release identity.
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "agent":
		runAgent(os.Args[2:])
	case "statusline":
		runStatusline(os.Args[2:])
	case "version":
		fmt.Println("omnitoken " + version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  omnitoken serve [-config ~/.omnitoken/config.json] [-listen :8787]
  omnitoken agent [-config ~/.omnitoken/agent.json] [-server http://HOST:8787] [-name NAME] [-token T] [-once] [-relay :8788]
  omnitoken statusline [-server http://HOST:8787] [-no-color]   # for Claude Code's statusLine hook`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "config file (JSON)")
	listen := fs.String("listen", "", "listen address override")
	fs.Parse(args)

	path := *configPath
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// First run: leave the user a file to edit instead of an invisible
		// set of defaults. A write failure (read-only home, odd -config path)
		// must not stop the server — fall back to running on pure defaults.
		if werr := server.WriteDefaultConfig(path); werr != nil {
			log.Printf("config: 无法写入 %s(%v),继续使用内置默认值", path, werr)
			path = ""
		} else {
			log.Printf("config: 已生成默认配置 %s,可编辑后重启生效", path)
		}
	}
	cfg, err := server.LoadConfig(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	log.Fatal(srv.Run())
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", filepath.Join(server.DataDir(), "agent.json"), "agent config file (JSON)")
	serverURL := fs.String("server", "", "server or relay base URL")
	token := fs.String("token", "", "ingest bearer token")
	name := fs.String("name", "", "device name (default: hostname)")
	once := fs.Bool("once", false, "single scan+report pass, then exit")
	relay := fs.String("relay", "", "also relay ingest for peers on this listen address (e.g. :8788)")
	proxyListen := fs.String("proxy-listen", "", "run the local API proxy on this address (e.g. 127.0.0.1:8899)")
	interval := fs.Int("interval", 0, "scan interval seconds (default 15)")
	dirs := fs.String("claude-dirs", "", "comma-separated Claude log dirs (default: auto-detect)")
	fs.Parse(args)

	fc, err := agent.LoadFileConfig(*configPath)
	if err != nil {
		log.Fatalf("config %s: %v", *configPath, err)
	}
	// Precedence: flag > env > config file > default.
	pick := func(flagVal, envKey, fileVal string) string {
		if flagVal != "" {
			return flagVal
		}
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		return fileVal
	}
	srvURL := pick(*serverURL, "OMNITOKEN_SERVER", fc.Server)
	if srvURL == "" {
		// Nothing to connect to. If there is no config file yet, leave a
		// skeleton so the fix is "edit this file", not "create one from the
		// docs". An existing file is never rewritten — it may hold a token.
		if _, statErr := os.Stat(*configPath); os.IsNotExist(statErr) {
			if werr := agent.WriteSkeletonConfig(*configPath); werr == nil {
				fmt.Fprintf(os.Stderr, "agent: 已生成配置骨架 %s —— 填入 \"server\" 后重新运行\n", *configPath)
				os.Exit(2)
			}
		}
		fmt.Fprintln(os.Stderr, "agent: server URL is required (-server flag, OMNITOKEN_SERVER env, or \"server\" in "+*configPath+")")
		os.Exit(2)
	}
	deviceName := pick(*name, "OMNITOKEN_NAME", fc.Name)
	if deviceName == "" {
		h, err := os.Hostname()
		if err != nil {
			log.Fatalf("hostname: %v", err)
		}
		deviceName = h
	}
	claudeDirs := fc.ClaudeDirs
	if *dirs != "" {
		claudeDirs = strings.Split(*dirs, ",")
	}
	if len(claudeDirs) == 0 {
		claudeDirs = server.DefaultLocalClaudeDirs()
	}
	codexDirs := fc.CodexDirs
	if len(codexDirs) == 0 {
		codexDirs = server.DefaultLocalCodexDirs()
	}
	intervalSec := *interval
	if intervalSec <= 0 {
		intervalSec = fc.IntervalSeconds
	}
	if intervalSec <= 0 {
		intervalSec = 15
	}
	statePath := fc.State
	if statePath == "" {
		statePath = filepath.Join(server.DataDir(), "agent-state.json")
	}
	a, err := agent.New(agent.Config{
		ServerURL:      strings.TrimSuffix(srvURL, "/"),
		Token:          pick(*token, "OMNITOKEN_TOKEN", fc.Token),
		DeviceName:     deviceName,
		ClaudeDirs:     claudeDirs,
		CodexDirs:      codexDirs,
		StatePath:      statePath,
		Interval:       time.Duration(intervalSec) * time.Second,
		RelayListen:    pick(*relay, "OMNITOKEN_RELAY", fc.RelayListen),
		ProxyListen:    pick(*proxyListen, "OMNITOKEN_PROXY", fc.ProxyListen),
		ProxyUpstreams: fc.ProxyUpstreams,
	})
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if *once {
		n, err := a.RunOnce()
		if err != nil {
			log.Fatalf("agent: %v", err)
		}
		log.Printf("agent: reported %d events", n)
		return
	}
	log.Fatal(a.Run())
}

// runStatusline must never fail loudly: Claude Code renders its output
// directly, so a broken server or config prints what it can and exits 0.
func runStatusline(args []string) {
	fs := flag.NewFlagSet("statusline", flag.ExitOnError)
	configPath := fs.String("config", filepath.Join(server.DataDir(), "statusline.json"), "statusline config file")
	serverURL := fs.String("server", "", "OmniToken server base URL")
	token := fs.String("token", "", "ingest bearer token (if the server requires one)")
	noColor := fs.Bool("no-color", false, "disable ANSI colour")
	fs.Parse(args)

	cfg, err := statusline.LoadConfig(*configPath)
	if err != nil {
		cfg = statusline.Config{} // unreadable config must not break the line
	}
	if *serverURL != "" {
		cfg.Server = *serverURL
	} else if v := os.Getenv("OMNITOKEN_SERVER"); v != "" && cfg.Server == "" {
		cfg.Server = v
	}
	if *token != "" {
		cfg.Token = *token
	} else if v := os.Getenv("OMNITOKEN_TOKEN"); v != "" && cfg.Token == "" {
		cfg.Token = v
	}
	if *noColor {
		cfg.NoColor = true
	}
	statusline.Run(cfg, os.Stdin, os.Stdout)
}

func defaultConfigPath() string {
	return filepath.Join(server.DataDir(), "config.json")
}
