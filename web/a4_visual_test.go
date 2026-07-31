package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestA4NormativeDarkPaletteAcrossSurfaces(t *testing.T) {
	want := map[string]string{
		"--page":           "#090b15",
		"--surface-1":      "#181a36",
		"--surface-2":      "#0d1022",
		"--text-primary":   "#f7f8ff",
		"--source-claude":  "#f2a16c",
		"--source-codex":   "#67c9ff",
		"--source-api":     "#a982ee",
		"--status-healthy": "#67e4b9",
		"--peak":           "#ff72a7",
	}

	for _, surface := range []string{"web", "desktop"} {
		t.Run(surface, func(t *testing.T) {
			css := readA4CSS(t, surface, "tokens.css")

			if !regexp.MustCompile(`(?m)^\s*color-scheme\s*:\s*dark\s*;`).MatchString(css) {
				t.Error("tokens.css must declare a dark color-scheme")
			}

			for token, expected := range want {
				actual, ok := firstA4Declaration(css, token)
				if !ok {
					t.Errorf("tokens.css is missing normative token %s: %s", token, expected)
					continue
				}
				if !strings.EqualFold(actual, expected) {
					t.Errorf("%s = %s, want %s", token, actual, expected)
				}
			}
		})
	}
}

func TestA4AmbientDepthAndOuterSurfaceAcrossSurfaces(t *testing.T) {
	cases := []struct {
		surface      string
		outerSurface string
	}{
		{surface: "web", outerSurface: `\.viz-root`},
		{surface: "desktop", outerSurface: `\.panel`},
	}

	for _, tc := range cases {
		t.Run(tc.surface, func(t *testing.T) {
			css := readA4CSS(t, tc.surface, "style.css")

			if count := strings.Count(css, "radial-gradient("); count < 2 {
				t.Errorf("style.css has %d ambient radial gradients, want at least 2", count)
			}

			outerRadius := regexp.MustCompile(
				fmt.Sprintf(`(?s)%s\s*\{[^}]*border-radius\s*:\s*24px\s*;`, tc.outerSurface),
			)
			if !outerRadius.MatchString(css) {
				t.Error("outer surface must use a 24px border radius")
			}
		})
	}
}

func TestA4SourceBarsUseGradientsAcrossSurfaces(t *testing.T) {
	sourceTokens := []string{
		"--source-claude",
		"--source-codex",
		"--source-api",
	}

	for _, surface := range []string{"web", "desktop"} {
		t.Run(surface, func(t *testing.T) {
			css := readA4CSS(t, surface, "style.css")
			for _, token := range sourceTokens {
				if !a4GradientUsesToken(css, token) {
					t.Errorf("source bars must use a gradient derived from var(%s)", token)
				}
			}
		})
	}
}

func readA4CSS(t *testing.T, surface, name string) string {
	t.Helper()

	var (
		data []byte
		err  error
	)
	switch surface {
	case "web":
		data, err = FS.ReadFile(name)
	case "desktop":
		data, err = os.ReadFile(filepath.Join("..", "desktop", "ui", name))
	default:
		t.Fatalf("unknown surface %q", surface)
	}
	if err != nil {
		t.Fatalf("read %s %s: %v", surface, name, err)
	}
	return string(data)
}

func firstA4Declaration(css, property string) (string, bool) {
	declaration := regexp.MustCompile(
		fmt.Sprintf(`(?m)^\s*%s\s*:\s*([^;]+);`, regexp.QuoteMeta(property)),
	)
	match := declaration.FindStringSubmatch(css)
	if len(match) != 2 {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func a4GradientUsesToken(css, token string) bool {
	for _, declaration := range strings.Split(css, ";") {
		if strings.Contains(declaration, "gradient(") &&
			strings.Contains(declaration, "var("+token+")") {
			return true
		}
	}
	return false
}
