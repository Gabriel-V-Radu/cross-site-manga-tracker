package handlers_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The stylesheet is the one part of the dashboard no Go test can otherwise
// reach: the suite renders templates and asserts on markup, but nothing
// evaluates CSS, so a stylesheet can break every control on the page while the
// whole suite stays green. That is not hypothetical — a palette pass once
// defined a token as itself (--line-strong: var(--line-strong)), which CSS
// treats as invalid at computed-value time, and every "border: 1px solid
// var(--line-strong)" declaration was dropped whole. Edit, Delete and Set last
// read rendered as bare text and the suite never noticed.

var (
	cssDefinitionPattern = regexp.MustCompile(`(--[A-Za-z0-9_-]+)\s*:\s*([^;}"]*)`)
	cssUsagePattern      = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)\s*[,)]`)
	// A var() with no fallback is the dangerous form: an undefined token voids
	// the whole declaration. With a fallback the declaration still resolves, so
	// a token supplied at runtime (an inline style, a setProperty call) is fine.
	cssRequiredUsagePattern = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)\s*\)`)
	jsSetPropertyPattern    = regexp.MustCompile(`setProperty\(\s*['"](--[A-Za-z0-9_-]+)['"]`)
)

func webDir(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file to resolve the web asset paths")
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "web")
}

func readDashboardCSS(t *testing.T) string {
	t.Helper()

	path := filepath.Join(webDir(t), "assets", "dashboard.css")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// runtimeSuppliedTokens collects the custom properties the page sets outside
// the stylesheet: inline style attributes in the templates and setProperty
// calls in the scripts. They are as defined as anything in :root.
func runtimeSuppliedTokens(t *testing.T) map[string]struct{} {
	t.Helper()

	tokens := map[string]struct{}{}
	for _, pattern := range []string{
		filepath.Join(webDir(t), "templates", "*.html"),
		filepath.Join(webDir(t), "assets", "*.js"),
	} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			text := string(raw)
			for _, match := range cssDefinitionPattern.FindAllStringSubmatch(text, -1) {
				tokens[match[1]] = struct{}{}
			}
			for _, match := range jsSetPropertyPattern.FindAllStringSubmatch(text, -1) {
				tokens[match[1]] = struct{}{}
			}
		}
	}
	return tokens
}

// definedCustomProperties maps each token to the values it is assigned. A
// token can legitimately be declared more than once (a media query overriding
// the light value, say), so every assignment is kept.
func definedCustomProperties(css string) map[string][]string {
	defined := map[string][]string{}
	for _, match := range cssDefinitionPattern.FindAllStringSubmatch(css, -1) {
		name := match[1]
		value := strings.TrimSpace(match[2])
		defined[name] = append(defined[name], value)
	}
	return defined
}

func TestDashboardCSSTokensAreNotSelfReferential(t *testing.T) {
	css := readDashboardCSS(t)

	for name, values := range definedCustomProperties(css) {
		for _, value := range values {
			for _, used := range cssUsagePattern.FindAllStringSubmatch(value, -1) {
				if used[1] == name {
					t.Errorf("%s is defined as %q, which references itself; CSS discards a cyclic token and every declaration using it, so anything styled with %s silently loses that property", name, value, name)
				}
			}
		}
	}
}

func TestDashboardCSSUsesOnlyDefinedTokens(t *testing.T) {
	css := readDashboardCSS(t)
	defined := definedCustomProperties(css)
	runtimeTokens := runtimeSuppliedTokens(t)

	missing := map[string]struct{}{}
	for _, match := range cssRequiredUsagePattern.FindAllStringSubmatch(css, -1) {
		name := match[1]
		if _, ok := defined[name]; ok {
			continue
		}
		if _, ok := runtimeTokens[name]; ok {
			continue
		}
		missing[name] = struct{}{}
	}

	if len(missing) == 0 {
		return
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Errorf("stylesheet uses undefined custom properties %v with no fallback; each one voids the whole declaration it appears in. Either define it, or write var(%s, <fallback>) if it is supplied at runtime", names, names[0])
}

// TestDashboardCSSTokenGuardCatchesACyclicToken proves the guard above would
// actually fail on the bug it exists for, rather than passing vacuously.
func TestDashboardCSSTokenGuardCatchesACyclicToken(t *testing.T) {
	broken := `:root { --line: #2d3a50; --line-strong: var(--line-strong); }
.mini-btn { border: 1px solid var(--line-strong); }`

	selfReferential := 0
	for name, values := range definedCustomProperties(broken) {
		for _, value := range values {
			for _, used := range cssUsagePattern.FindAllStringSubmatch(value, -1) {
				if used[1] == name {
					selfReferential++
				}
			}
		}
	}
	if selfReferential != 1 {
		t.Fatalf("the cyclic-token check found %d self-references in a stylesheet that has exactly one", selfReferential)
	}

	defined := definedCustomProperties(":root { --a: red; }")
	if _, ok := defined["--a"]; !ok {
		t.Fatal("a plain token definition must be recognised")
	}
	if _, ok := defined["--missing"]; ok {
		t.Fatal("an undefined token must not be reported as defined")
	}
}
