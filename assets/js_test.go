package assets

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/dop251/goja"
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

// The editor's layout, crop and text-wrapping maths are plain functions, so they
// run here in a JavaScript interpreter without a browser.
func TestEditorMathRuns(t *testing.T) {
	src, err := fs.ReadFile(FS, "static/editor.js")
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if err := vm.Set("window", vm.NewObject()); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(string(src)); err != nil {
		t.Fatalf("editor.js does not run: %v", err)
	}
	eval := func(js string) string {
		t.Helper()
		v, err := vm.RunString("JSON.stringify(" + js + ")")
		if err != nil {
			t.Fatalf("%s: %v", js, err)
		}
		return v.String()
	}
	for js, want := range map[string]string{
		`window.TVTEditor.pure.outputSize("auto", 1600, 900, "single")`:                          `{"w":1280,"h":720}`,
		`window.TVTEditor.pure.outputSize("auto", 1000, 2000, "single")`:                         `{"w":960,"h":1920}`,
		`window.TVTEditor.pure.outputSize("auto", 800, 800, "grid4")`:                            `{"w":1280,"h":720}`,
		`window.TVTEditor.pure.outputSize("9:16", 0, 0, "single")`:                               `{"w":1080,"h":1920}`,
		`window.TVTEditor.pure.outputSize("1:1", 0, 0, "single")`:                                `{"w":1280,"h":1280}`,
		`window.TVTEditor.pure.cellRects("h2", 1000, 500, 20)[0]`:                                `{"x":10,"y":10,"w":480,"h":480}`,
		`window.TVTEditor.pure.cellRects("h2", 1000, 500, 20)[1]`:                                `{"x":510,"y":10,"w":480,"h":480}`,
		`window.TVTEditor.pure.cellRects("grid4", 100, 100, 0).length`:                           `4`,
		`window.TVTEditor.pure.cellRects("nope", 100, 100, 0).length`:                            `1`,
		`window.TVTEditor.pure.coverRect(200, 100, {x:0,y:0,w:100,h:100}, 1, 0, 0)`:              `{"x":-50,"y":0,"w":200,"h":100,"slackX":50,"slackY":0}`,
		`window.TVTEditor.pure.coverRect(200, 100, {x:0,y:0,w:100,h:100}, 1, 1, 0).x`:            `0`,
		`window.TVTEditor.pure.coverRect(200, 100, {x:0,y:0,w:100,h:100}, 1, -9, 0).x`:           `-100`,
		`window.TVTEditor.pure.coverRect(200, 100, {x:0,y:0,w:100,h:100}, 2, 0, 0).h`:            `200`,
		`window.TVTEditor.pure.wrapLines((s) => s.length * 10, "hello world foo", 100)`:          `["hello","world foo"]`,
		`window.TVTEditor.pure.wrapLines((s) => s.length * 10, "abcdefghijklmnop", 100)`:         `["abcdefghij","klmnop"]`,
		`window.TVTEditor.pure.wrapLines((s) => s.length * 10, "one\n\ntwo", 100)`:               `["one","","two"]`,
		`window.TVTEditor.pure.wrapLines((s) => s.length * 10, "", 100)`:                         `[""]`,
		`Object.keys(window.TVTEditor.pure.LAYOUTS).length + ":" + typeof window.TVTEditor.open`: `"8:function"`,
	} {
		if got := eval(js); got != want {
			t.Errorf("%s\n got  %s\n want %s", js, got, want)
		}
	}
}
