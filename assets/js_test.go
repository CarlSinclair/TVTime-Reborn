package assets

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/dop251/goja/parser"
)

// The frontend has no build step and no Node in CI, so this parses every
// embedded script and fails on a syntax error that would otherwise only show up
// as a blank page in a browser.
func TestEmbeddedScriptsParse(t *testing.T) {
	scripts, err := fs.Glob(FS, "static/*.js")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no embedded scripts found: %v", err)
	}
	for _, name := range scripts {
		src, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseFile(nil, name, string(src), 0); err != nil {
			t.Errorf("%s does not parse: %v", name, err)
		}
	}
}

// Project rule: no comments in frontend files.
func TestFrontendFilesHaveNoComments(t *testing.T) {
	for _, pattern := range []string{"static/*.js", "static/*.css", "templates/*.html", "templates/partials/*.html"} {
		files, _ := fs.Glob(FS, pattern)
		for _, name := range files {
			b, err := fs.ReadFile(FS, name)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			switch {
			case strings.HasSuffix(name, ".html"):
				if strings.Contains(s, "<!--") {
					t.Errorf("%s contains an HTML comment", name)
				}
			case strings.HasSuffix(name, ".css"):
				if strings.Contains(s, "/*") {
					t.Errorf("%s contains a CSS comment", name)
				}
			case strings.HasSuffix(name, ".js"):
				for i, line := range strings.Split(s, "\n") {
					l := strings.TrimSpace(line)
					if strings.HasPrefix(l, "//") || strings.HasPrefix(l, "/*") {
						t.Errorf("%s:%d contains a comment", name, i+1)
					}
				}
			}
		}
	}
}

func TestParserRejectsBrokenScript(t *testing.T) {
	for _, bad := range []string{"const x = ;", "function (", "async () => { await }", "if (a { }"} {
		if _, err := parser.ParseFile(nil, "bad.js", bad, 0); err == nil {
			t.Errorf("parser accepted broken script %q, so the syntax test proves nothing", bad)
		}
	}
	for _, good := range []string{"const f = async (a) => { await a?.b; };", "for (const [k, v] of Object.entries({})) { }"} {
		if _, err := parser.ParseFile(nil, "good.js", good, 0); err != nil {
			t.Errorf("parser rejected modern syntax %q: %v", good, err)
		}
	}
}
