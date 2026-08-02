package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Installing the status-line hook (ADR-0011).
//
// abtop does the same job by writing its own script straight into
// settings.json's statusLine slot. Two things about that are worth not
// copying: it replaces whatever command was there (and its script prints
// nothing, so the status bar goes blank), and it ships no way to undo the
// change. Here an existing command is wrapped rather than displaced, and
// Undo puts it back.

const (
	hookScriptName = "statusline-hook.sh"
	hookStateName  = "statusline-hook.json"
)

// hookState remembers what the slot held before, so Undo is exact rather than
// a guess at what the user probably had.
type hookState struct {
	WrappedCommand string `json:"wrapped_command"` // "" = the slot was empty
	InstalledAt    int64  `json:"installed_at"`
	ScriptPath     string `json:"script_path"`
}

// SetupResult describes what an install did, for the CLI to report.
type SetupResult struct {
	SettingsPath string
	ScriptPath   string // "" when the command was registered directly
	Wrapped      string // the pre-existing command now called by the wrapper
	AlreadyDone  bool
	BackupPath   string
}

// ClaudeSettingsPath locates Claude Code's settings, honouring the same
// CLAUDE_CONFIG_DIR override Claude Code itself uses.
func ClaudeSettingsPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return filepath.Join(dir, "settings.json"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// Setup registers the hook, wrapping whatever command already occupies the
// slot. selfPath is this binary's absolute path — resolved by the caller so
// the script does not depend on PATH.
func Setup(settingsPath, dataDir, selfPath string, now time.Time) (SetupResult, error) {
	res := SetupResult{SettingsPath: settingsPath}

	settings, err := readSettings(settingsPath)
	if err != nil {
		return res, err
	}
	existing := existingCommand(settings)
	scriptPath := filepath.Join(dataDir, hookScriptName)

	switch {
	case existing == scriptPath:
		res.AlreadyDone, res.ScriptPath = true, scriptPath
		if st, err := readHookState(dataDir); err == nil {
			res.Wrapped = st.WrappedCommand
		}
		return res, nil
	case existing != "" && strings.Contains(existing, "omnitoken statusline"):
		// Already ours and already rendering; wrapping it would only add a
		// process for no gain.
		res.AlreadyDone = true
		return res, nil
	}

	// Refuse to guess with a command we cannot reproduce in a shell script.
	if strings.ContainsAny(existing, "\n\r") {
		return res, fmt.Errorf("existing statusLine command spans multiple lines; "+
			"wrap it by hand instead (see docs/configuration.md): %q", existing)
	}

	if existing == "" {
		// Nothing to preserve: OmniToken can render the line itself.
		res.BackupPath, err = writeSettings(settingsPath, settings, selfPath+" statusline", now)
		if err != nil {
			return res, err
		}
		return res, saveHookState(dataDir, hookState{InstalledAt: now.UnixMilli()})
	}

	if err := writeHookScript(scriptPath, selfPath, existing); err != nil {
		return res, err
	}
	res.BackupPath, err = writeSettings(settingsPath, settings, scriptPath, now)
	if err != nil {
		return res, err
	}
	res.ScriptPath, res.Wrapped = scriptPath, existing
	return res, saveHookState(dataDir, hookState{
		WrappedCommand: existing, InstalledAt: now.UnixMilli(), ScriptPath: scriptPath,
	})
}

// Undo restores the command the slot held before Setup and removes the script.
func Undo(settingsPath, dataDir string, now time.Time) (string, error) {
	st, err := readHookState(dataDir)
	if err != nil {
		return "", fmt.Errorf("no record of an install to undo (%s): %w",
			filepath.Join(dataDir, hookStateName), err)
	}
	settings, err := readSettings(settingsPath)
	if err != nil {
		return "", err
	}
	if _, err := writeSettings(settingsPath, settings, st.WrappedCommand, now); err != nil {
		return "", err
	}
	if st.ScriptPath != "" {
		os.Remove(st.ScriptPath)
	}
	os.Remove(filepath.Join(dataDir, hookStateName))
	return st.WrappedCommand, nil
}

// readSettings parses settings.json, treating a missing file as empty and a
// malformed one as fatal. Rewriting JSON we failed to understand would destroy
// settings that have nothing to do with us.
func readSettings(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, refusing to rewrite it: %w", path, err)
	}
	return out, nil
}

func existingCommand(settings map[string]any) string {
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return ""
	}
	cmd, _ := sl["command"].(string)
	return strings.TrimSpace(cmd)
}

// writeSettings sets statusLine.command, keeping every sibling key — including
// any statusLine options such as padding or refreshInterval. Returns the backup
// path. An empty command removes the whole statusLine block.
func writeSettings(path string, settings map[string]any, command string, now time.Time) (string, error) {
	if command == "" {
		delete(settings, "statusLine")
	} else {
		sl, _ := settings["statusLine"].(map[string]any)
		if sl == nil {
			sl = map[string]any{"type": "command"}
		}
		sl["command"] = command
		settings["statusLine"] = sl
	}

	var backup string
	if raw, err := os.ReadFile(path); err == nil {
		backup = fmt.Sprintf("%s.bak-%d", path, now.Unix())
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return "", err
		}
	}
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return backup, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return backup, err
	}
	return backup, os.WriteFile(path, append(body, '\n'), 0o644)
}

// writeHookScript emits the wrapper. stdin can only be consumed once, so it is
// read into a variable and handed to both consumers.
func writeHookScript(path, selfPath, wrapped string) error {
	script := `#!/bin/sh
# OmniToken status-line hook. Installed by ` + "`omnitoken statusline -setup`" + `.
# Undo with ` + "`omnitoken statusline -setup-undo`" + `.
#
# Claude Code allows one statusLine command, but OmniToken only needs to see
# the payload — not to render it. So the original command below still draws
# your status line; OmniToken just reads the quota out of the same bytes.
#
# stdin can only be read once, hence the variable.
input=$(cat)

# Capture is best-effort: it must never break the line it is riding along with.
printf '%s' "$input" | ` + shellQuote(selfPath) + ` statusline -capture-only >/dev/null 2>&1

printf '%s' "$input" | ` + wrapped + `
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(script), 0o700)
}

// shellQuote wraps a path in single quotes so spaces and the like survive.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func saveHookState(dataDir string, st hookState) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, hookStateName), body, 0o600)
}

func readHookState(dataDir string) (hookState, error) {
	var st hookState
	raw, err := os.ReadFile(filepath.Join(dataDir, hookStateName))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}
