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
	"io"
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
		if len(os.Args) >= 3 && os.Args[2] == "enroll" {
			if err := runAgentEnrollWith(os.Args[3:], os.Stdout); err != nil {
				log.Fatalf("agent enroll: %v", err)
			}
		} else {
			runAgent(os.Args[2:])
		}
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
  omnitoken serve [-config ~/.omnitoken/config.json] [-listen :8787] [-name NAME] [-rescan]
  omnitoken agent enroll [-config ~/.omnitoken/agent.json] [-server URL] [-name NAME] [-allow-insecure-http]
  omnitoken agent [-config ~/.omnitoken/agent.json] [-server http://HOST:8787] [-name NAME] [-token T] [-once] [-relay :8788] [-rescan]
  omnitoken statusline [-server http://HOST:8787] [-no-color]   # for Claude Code's statusLine hook
  omnitoken statusline -capture-only                            # quota only, keep your own status line
  omnitoken statusline -setup [-setup-undo]                     # install/remove the Claude Code hook`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "config file (JSON)")
	listen := fs.String("listen", "", "listen address override")
	name := fs.String("name", "", "device name override (default: device_name in config, else hostname)")
	rescan := fs.Bool("rescan", false, "re-read all local logs from the start once, backfilling derived fields")
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
	// Before the store opens: this decides where a day starts for every
	// aggregation, on both the Go and the SQL side (ADR-0021). Always logged —
	// following the host is a legitimate choice, but it should be a recorded
	// one rather than a default nobody ever looked at.
	if zone := server.ApplyTimezone(cfg); cfg.Timezone == "" {
		// `time.Local.String()` is just "Local" when TZ is unset, which says
		// nothing. Name the offset actually in force instead — the whole point
		// of logging this is that an unconfigured zone stops being invisible.
		abbr, offset := time.Now().Zone()
		log.Printf("timezone: 未配置,跟随主机时区(当前 %s,UTC%+03d:%02d);"+
			"日/周/月的切分以它为准(ADR-0021)", abbr, offset/3600, abs(offset%3600)/60)
	} else {
		log.Printf("timezone: %s —— 日/周/月的切分以它为准(ADR-0021)", zone)
	}
	logDeviceIdentity(cfg.ResolveDeviceName(*name))
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if *rescan {
		n, err := srv.ResetOffsets()
		if err != nil {
			log.Fatalf("rescan: %v", err)
		}
		// Worth saying out loud: this looks alarming and is not. Re-import is
		// idempotent on event_id, so the pass ahead fills in derived fields
		// (ADR-0009's gen_ms) and cannot add a count. It can now REMOVE one, and
		// exactly one thing does that: a Codex fork that copied a generation
		// into a second rollout is collapsed back to one row (ADR-0020). That
		// happens once; every rescan after it reports nothing.
		log.Printf("rescan: 已清空 %d 个文件的读取位点,本次启动将重扫全部本地日志"+
			"(幂等:回填派生字段;首次还会清掉 Codex 分叉重复计入的行,见 ADR-0020)", n)
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
	rescan := fs.Bool("rescan", false, "re-read all local logs from the start once, backfilling derived fields")
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
	// The same chain `serve` uses, through the same function (ADR-0019 §7).
	// This used to stop at agent.json's `name` and fall straight to the
	// hostname, which is how one Mac ended up in the database twice.
	identity := server.LocalDeviceName(*name, fc.Name, defaultConfigPath())
	logDeviceIdentity(identity)
	deviceName := identity.Name
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
	// LoadFileConfig already rejected a malformed date, so this cannot fail
	// here; checking anyway keeps the assumption from going unstated.
	since, err := fc.SinceTime()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	a, err := agent.New(agent.Config{
		ServerURL:          strings.TrimSuffix(srvURL, "/"),
		AllowInsecureHTTP:  fc.AllowInsecureHTTP,
		Token:              pick(*token, "OMNITOKEN_TOKEN", fc.Token),
		ProtocolVersion:    fc.EffectiveProtocolVersion(),
		DeviceID:           fc.DeviceID,
		DeviceToken:        pick("", "OMNITOKEN_DEVICE_TOKEN", fc.DeviceToken),
		OutboxPath:         fc.Outbox,
		OutboxMaxBytes:     fc.OutboxMaxBytes,
		AgentVersion:       version,
		DeviceName:         deviceName,
		Since:              since,
		ClaudeDirs:         claudeDirs,
		CodexDirs:          codexDirs,
		StatePath:          statePath,
		Interval:           time.Duration(intervalSec) * time.Second,
		RelayListen:        pick(*relay, "OMNITOKEN_RELAY", fc.RelayListen),
		RelayToken:         pick("", "OMNITOKEN_RELAY_TOKEN", fc.RelayToken),
		RelayUpstreamToken: pick("", "OMNITOKEN_RELAY_UPSTREAM_TOKEN", fc.RelayUpstreamToken),
		ProxyListen:        pick(*proxyListen, "OMNITOKEN_PROXY", fc.ProxyListen),
		ProxyUpstreams:     fc.ProxyUpstreams,
	})
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	// Before either mode: with -once this makes the single pass a full
	// re-import, which is what a cron-driven backfill wants.
	if *rescan {
		n, err := a.ResetOffsets()
		if err != nil {
			log.Fatalf("rescan: %v", err)
		}
		// Same caveat as serve's: idempotent, except that the first pass after
		// ADR-0020 collapses Codex's forked copies back to one row each.
		log.Printf("rescan: 已清空 %d 个文件的读取位点,本次将重扫全部本地日志"+
			"(幂等:回填派生字段;首次还会清掉 Codex 分叉重复计入的行,见 ADR-0020)", n)
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

func runAgentEnrollWith(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("agent enroll", flag.ContinueOnError)
	fs.SetOutput(output)
	configPath := fs.String("config", filepath.Join(server.DataDir(), "agent.json"), "agent config file (JSON)")
	serverURL := fs.String("server", "", "hub base URL")
	name := fs.String("name", "", "device display name (default: hostname)")
	allowInsecureHTTP := fs.Bool("allow-insecure-http", false, "allow plaintext HTTP to a non-loopback hub")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fc, err := agent.LoadFileConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pick := func(flagValue, envKey, fileValue string) string {
		if flagValue != "" {
			return flagValue
		}
		if value := os.Getenv(envKey); value != "" {
			return value
		}
		return fileValue
	}
	hub := pick(*serverURL, "OMNITOKEN_SERVER", fc.Server)
	if hub == "" {
		return fmt.Errorf("server URL is required")
	}
	admin := os.Getenv("OMNITOKEN_ADMIN_TOKEN")
	if admin == "" {
		return fmt.Errorf("admin credential is required")
	}
	// Enrolling under a different name than the one the agent reports with would
	// register a second identity on purpose; both come from the same chain.
	displayName := server.LocalDeviceName(*name, fc.Name, defaultConfigPath()).Name
	candidate, err := agent.PrepareEnrollment(
		fc,
		strings.TrimSuffix(hub, "/"),
		displayName,
		os.Getenv("OMNITOKEN_DEVICE_TOKEN"),
	)
	if err != nil {
		return err
	}
	if *allowInsecureHTTP {
		candidate.AllowInsecureHTTP = true
	}
	if err := agent.Enroll(candidate.Server, admin, candidate, nil); err != nil {
		return err
	}
	if err := agent.SaveFileConfig(*configPath, candidate); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(output, "Enrolled device %s (%s); configuration saved to %s\n",
		candidate.DeviceID, candidate.Name, *configPath)
	return nil
}

// runStatusline must never fail loudly: Claude Code renders its output
// directly, so a broken server or config prints what it can and exits 0.
func runStatusline(args []string) {
	fs := flag.NewFlagSet("statusline", flag.ExitOnError)
	configPath := fs.String("config", filepath.Join(server.DataDir(), "statusline.json"), "statusline config file")
	serverURL := fs.String("server", "", "OmniToken server base URL")
	token := fs.String("token", "", "ingest bearer token (if the server requires one)")
	noColor := fs.Bool("no-color", false, "disable ANSI colour")
	captureOnly := fs.Bool("capture-only", false,
		"capture quota from the payload and print nothing (keep your own status line)")
	setup := fs.Bool("setup", false,
		"install the Claude Code hook, wrapping any status line you already use")
	setupUndo := fs.Bool("setup-undo", false, "restore the status line -setup replaced")
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
	if *setup || *setupUndo {
		runStatuslineSetup(*setupUndo)
		return
	}
	if *captureOnly {
		statusline.Capture(cfg, os.Stdin)
		return
	}
	statusline.Run(cfg, os.Stdin, os.Stdout)
}

// runStatuslineSetup wires the hook into Claude Code's settings, or unwires it.
// Unlike the render path this one talks to the user and exits non-zero on
// failure: it is an explicit administrative action, not something riding along
// with a status bar.
func runStatuslineSetup(undo bool) {
	settingsPath, err := statusline.ClaudeSettingsPath()
	if err != nil {
		log.Fatalf("statusline setup: %v", err)
	}
	dataDir := server.DataDir()

	if undo {
		restored, err := statusline.Undo(settingsPath, dataDir, time.Now())
		if err != nil {
			log.Fatalf("statusline setup-undo: %v", err)
		}
		if restored == "" {
			fmt.Printf("Removed the statusLine entry from %s.\n", settingsPath)
		} else {
			fmt.Printf("Restored your status line in %s:\n  %s\n", settingsPath, restored)
		}
		return
	}

	res, err := statusline.Setup(settingsPath, dataDir, selfPath(), time.Now())
	if err != nil {
		log.Fatalf("statusline setup: %v", err)
	}
	switch {
	case res.AlreadyDone:
		fmt.Println("Already installed; nothing to change.")
	case res.Wrapped != "":
		fmt.Printf("Kept your status line and added quota capture beside it.\n"+
			"  settings: %s\n  wrapper:  %s\n  still rendering: %s\n",
			res.SettingsPath, res.ScriptPath, res.Wrapped)
	default:
		fmt.Printf("Installed OmniToken as your status line.\n  settings: %s\n", res.SettingsPath)
	}
	if res.BackupPath != "" {
		fmt.Printf("  backup:   %s\n", res.BackupPath)
	}
	fmt.Println("Restart Claude Code (or wait for the next status-line refresh) to start collecting quota.")
	fmt.Println("Undo with: omnitoken statusline -setup-undo")
}

// selfPath resolves this binary absolutely so the generated script does not
// depend on PATH; falling back to the bare name is better than failing setup.
func selfPath() string {
	if p, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return "omnitoken"
}

func defaultConfigPath() string {
	return filepath.Join(server.DataDir(), "config.json")
}

// logDeviceIdentity says out loud when this machine's name was not configured
// but adopted (ADR-0019 §7.2). The hostname fallback is kept deliberately —
// a headless server should not refuse to start over a display string — so the
// remaining defence is that the user hears about it here, rather than by
// noticing one machine listed twice in the panel weeks later.
func logDeviceIdentity(identity server.DeviceIdentity) {
	switch identity.Source {
	case server.DeviceNameFromHostname:
		log.Printf("device: 未配置 device_name,使用主机名 %q。"+
			"若这台机器已用别的名字入过库,面板上会出现两个身份 —— 可在设置页合并(ADR-0019)",
			identity.Name)
	case server.DeviceNameLastResort:
		log.Printf("device: 未配置 device_name 且读不到主机名,退回 %q", identity.Name)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
