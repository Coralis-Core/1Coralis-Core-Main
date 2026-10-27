// Package archtest enforces the folder/dependency rules so the structure can
// never silently drift: no core knows another core, no core knows the app,
// the base knows nothing about anything above it, and nothing in this module
// is tied to one specific program/platform/vendor.
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const module = "example.com/project"

// layerOf maps a module-relative directory to a layer name.
func layerOf(rel string) string {
	p := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case len(p) >= 2 && p[0] == "engine" && p[1] == "base":
		return "base"
	case len(p) >= 2 && p[0] == "engine" && p[1] == "verify":
		return "verify"
	case len(p) >= 2 && p[0] == "engine" && p[1] == "cmd":
		return "cmd" // generic programs shipped with the engine
	case p[0] == "contracts":
		return "contracts"
	case p[0] == "cores" && len(p) >= 2:
		return "core:" + p[1]
	case p[0] == "app" && len(p) >= 2 && !strings.HasSuffix(p[1], ".go"):
		return "app:" + p[1] // frontend, backend, config, ...any folder you add
	case p[0] == "app":
		return "app-root" // app/main.go: the one composition root
	}
	return "other"
}

// allowed says whether code in layer `from` may import a package in layer `to`.
func allowed(from, to string) bool {
	if from == to {
		return true
	}
	switch {
	case from == "verify":
		return true // tooling may look at everything
	case to == "base":
		return true // everyone may use the base
	case to == "contracts":
		return from != "base" // contracts sit above the base
	case from == "cmd":
		return false // may use base + contracts (handled above); nothing else
	case from == "app-root":
		return to != "verify" // the composition root wires cores and app parts together
	}
	// everything else (base, contracts, a core, an app folder) may not reach sideways or up:
	// no core -> other core, no core -> app, no app folder -> cores or other app folders.
	return false
}

// Nothing in this module may be tied to a concrete program, platform or vendor. Put the import paths
// (or fragments of them) of any concrete program your deployment must never leak into the base here.
var banned = []string{"vendor.example/", "program-x"}

type violation struct{ file, msg string }

func scan(root string) ([]violation, error) {
	var out []violation
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir // _examples, _template: documentation, not part of the build graph
			}
			if path != root {
				if _, e := os.Stat(filepath.Join(path, "go.mod")); e == nil {
					return filepath.SkipDir // nested module (own go.mod, own deps), e.g. app/frontend/web
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "app") && !strings.Contains(strings.TrimPrefix(rel, "app"), "/") && rel == "app" {
			rel = "app/" + filepath.Base(path) // a .go file directly in app/ => composition root
		}
		from := layerOf(rel)
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		relFile, _ := filepath.Rel(root, path)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, b := range banned {
				if strings.Contains(p, b) {
					out = append(out, violation{relFile, "imports banned " + p})
				}
			}
			if !strings.HasPrefix(p, module+"/") {
				continue
			}
			target := strings.TrimPrefix(p, module+"/")
			to := layerOf(target)
			if !allowed(from, to) {
				out = append(out, violation{relFile, "layer " + from + " must not import " + to + " (" + p + ")"})
			}
		}
		return nil
	})
	return out, err
}

func TestFolderRules(t *testing.T) {
	v, err := scan("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range v {
		t.Errorf("%s: %s", x.file, x.msg)
	}
}

func TestRulesHaveTeeth(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"base", "contracts", false},
		{"base", "cores", false},
		{"contracts", "base", true},
		{"core:a", "core:b", false},
		{"core:a", "core:a", true},
		{"core:a", "contracts", true},
		{"core:a", "app:backend", false},
		{"app:backend", "core:a", false},
		{"app:backend", "app:frontend", false},
		{"app:frontend", "contracts", true},
		{"cmd", "contracts", true},
		{"cmd", "core:a", false},
		{"cmd", "app:backend", false},
		{"app-root", "core:a", true},
		{"app-root", "app:backend", true},
		{"app-root", "verify", false},
		{"verify", "core:a", true},
	}
	for _, c := range cases {
		if got := allowed(c.from, c.to); got != c.want {
			t.Errorf("allowed(%s -> %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
	for rel, want := range map[string]string{
		"engine/base/kernel": "base", "engine/verify/conformance": "verify", "contracts/identity": "contracts",
		"cores/memory": "core:memory", "app/frontend/console": "app:frontend", "app/backend/product": "app:backend",
		"app/anything-i-add": "app:anything-i-add", "app/main.go": "app-root", "engine/cmd/platform": "cmd",
	} {
		if got := layerOf(rel); got != want {
			t.Errorf("layerOf(%s) = %s, want %s", rel, got, want)
		}
	}
}

// The base must stay free of any customer-facing or chat-like shape: whatever
// sits on top talks to it through capabilities and the wire contract only.
func TestBaseHasNoChatShape(t *testing.T) {
	bad := regexp.MustCompile(`\b(ChatID|UserID|chat_id|user_id)\b|engine/base/facade`)
	root := "../../base"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if m := bad.Find(b); m != nil {
			t.Errorf("%s contains a chat-shaped name %q: the base must stay domain-free", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
