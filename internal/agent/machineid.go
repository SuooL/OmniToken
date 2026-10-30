package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// MachineIdentity returns an application-specific identifier, never the raw OS
// identifier. It is independent of hostname, user, config path and credentials.
func MachineIdentity() (string, error) {
	var raw string
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "windows":
		var b []byte
		b, err = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_ComputerSystemProduct).UUID").Output()
		raw = string(b)
	case "darwin":
		var b []byte
		b, err = exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		match := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`).FindSubmatch(b)
		if len(match) == 2 {
			raw = string(match[1])
		}
	case "linux":
		var b []byte
		b, err = os.ReadFile("/etc/machine-id")
		raw = string(b)
	default:
		return "", fmt.Errorf("machine identity unavailable on %s", runtime.GOOS)
	}
	if err != nil {
		return "", fmt.Errorf("read machine identity: %w", err)
	}
	return machineIdentityFromRaw(raw)
}

func machineIdentityFromRaw(raw string) (string, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "-", "")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(normalized) || normalized == strings.Repeat("0", 32) || normalized == strings.Repeat("f", 32) {
		return "", fmt.Errorf("machine identity unavailable or invalid; repair the system machine identifier before enrollment")
	}
	digest := sha256.Sum256([]byte("omnitoken:machine:v1:" + normalized))
	digest[6] = (digest[6] & 0x0f) | 0x80
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16]), nil
}
