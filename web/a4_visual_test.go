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

func TestCompactTopNavigationContract(t *testing.T) {
	css := readA4CSS(t, "web", "style.css")
	html, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read web index: %v", err)
	}

	contracts := map[string]*regexp.Regexp{
		"12px desktop viewport inset": regexp.MustCompile(
			`(?s)body\s*\{[^}]*padding\s*:\s*12px\s*;`,
		),
		"1600px single-column shell": regexp.MustCompile(
			`(?s)\.viz-root\s*\{[^}]*width\s*:\s*min\(1600px,\s*100%\)\s*;[^}]*grid-template-columns\s*:\s*minmax\(0,\s*1fr\)\s*;`,
		),
		"horizontal top navigation": regexp.MustCompile(
			`(?s)\.rail\s*\{[^}]*flex-direction\s*:\s*row\s*;`,
		),
		"compact top navigation inset": regexp.MustCompile(
			`(?s)\.rail\s*\{[^}]*padding\s*:\s*10px\s+14px\s*;`,
		),
		"top navigation divider": regexp.MustCompile(
			`(?s)\.rail\s*\{[^}]*border-bottom\s*:\s*1px\s+solid\s+var\(--border\)\s*;`,
		),
		"right-aligned route row": regexp.MustCompile(
			`(?s)\.tabs\s*\{[^}]*margin-left\s*:\s*auto\s*;[^}]*flex-direction\s*:\s*row\s*;`,
		),
		"unclipped keyboard focus": regexp.MustCompile(
			`(?s)\.tabs\s+a:focus-visible\s*\{[^}]*outline-offset\s*:\s*-2px\s*;`,
		),
		"8px compact breakpoint inset": regexp.MustCompile(
			`(?s)@media\s*\(max-width:\s*860px\)\s*\{[^@]*?body\s*\{[^}]*padding\s*:\s*8px\s*;`,
		),
		"420px full-bleed shell": regexp.MustCompile(
			`(?s)@media\s*\(max-width:\s*420px\)\s*\{[^@]*?body\s*\{[^}]*padding\s*:\s*0\s*;[^@]*?\.viz-root\s*\{[^}]*border-inline\s*:\s*0\s*;[^}]*border-radius\s*:\s*0\s*;`,
		),
		"420px content inset": regexp.MustCompile(
			`(?s)@media\s*\(max-width:\s*420px\)\s*\{[^@]*?\.content\s*\{[^}]*padding\s*:\s*16px\s+12px\s+36px\s*;`,
		),
	}
	for label, contract := range contracts {
		if !contract.MatchString(css) {
			t.Errorf("compact navigation is missing %s", label)
		}
	}

	markup := string(html)
	wantRoutes := []string{
		"overview", "live", "speed", "reports", "details",
		"devices", "models", "cache", "settings",
	}
	if count := strings.Count(markup, `data-view="`); count != len(wantRoutes) {
		t.Fatalf("top navigation has %d route anchors, want %d", count, len(wantRoutes))
	}
	previous := -1
	for _, route := range wantRoutes {
		index := strings.Index(markup, `data-view="`+route+`"`)
		if index < 0 {
			t.Errorf("top navigation is missing %q", route)
			continue
		}
		if index <= previous {
			t.Errorf("top navigation route %q is out of order", route)
		}
		previous = index
	}
	if strings.Contains(markup, `class="tab-group"`) {
		t.Error("top navigation must not keep visible rail group labels")
	}
	if !strings.Contains(markup, `<h2 id="page-title">总览</h2>`) ||
		!strings.Contains(markup, `<p class="subtitle" id="page-sub">累计与趋势</p>`) {
		t.Error("static page head must match the default Overview route before JavaScript boots")
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
