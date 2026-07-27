package collect

import (
	"os/exec"
	"strings"
)

// NormalizeRepoURL maps the many spellings of one remote to a single identity,
// so the same repo cloned on different machines/paths aggregates together:
//
//	git@github.com:User/Repo.git  → github.com/user/repo
//	https://github.com/User/Repo  → github.com/user/repo
//	ssh://git@host:2222/a/b.git   → host/a/b
func NormalizeRepoURL(url string) string {
	u := strings.TrimSpace(url)
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	for _, p := range []string{"ssh://", "git://", "http://", "https://"} {
		u = strings.TrimPrefix(u, p)
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	// scp-like syntax: host:path
	if i := strings.Index(u, ":"); i >= 0 {
		rest := u[i+1:]
		if !strings.Contains(u[:i], "/") {
			// drop a port number if present, otherwise it's host:path
			if j := strings.IndexAny(rest, "/"); j >= 0 && isDigits(rest[:j]) {
				u = u[:i] + rest[j:]
			} else {
				u = u[:i] + "/" + rest
			}
		}
	}
	return strings.ToLower(u)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// LocalRepoResolver returns the normalized origin URL for a cwd on this
// machine, falling back to the git toplevel path for remoteless repos.
func LocalRepoResolver(cwd string) string {
	if out, ok := runGit(cwd, "config", "--get", "remote.origin.url"); ok && out != "" {
		return NormalizeRepoURL(out)
	}
	if out, ok := runGit(cwd, "rev-parse", "--show-toplevel"); ok && out != "" {
		return "local:" + out
	}
	return ""
}

// SSHRepoResolver probes a cwd on a remote host over the user's existing SSH
// access. Failures (host down, dir gone) resolve to "" and are cached; the
// cache entry can be cleared by deleting the state file.
func SSHRepoResolver(host string) func(cwd string) string {
	return func(cwd string) string {
		script := "git -C " + shQuote(cwd) + " config --get remote.origin.url 2>/dev/null" +
			" || git -C " + shQuote(cwd) + " rev-parse --show-toplevel 2>/dev/null | sed 's/^/local:/'"
		out, err := sshCommand(host, script).Output()
		if err != nil {
			return ""
		}
		res := strings.TrimSpace(string(out))
		if strings.HasPrefix(res, "local:") {
			return res
		}
		return NormalizeRepoURL(res)
	}
}

func runGit(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func sshCommand(host, script string) *exec.Cmd {
	return exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		host, script)
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
