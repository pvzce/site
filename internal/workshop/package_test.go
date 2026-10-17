package workshop

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
)

func archive(t *testing.T, files map[string]string) *os.File {
	t.Helper()
	f, e := os.CreateTemp(t.TempDir(), "pack-*.zip")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = f.Close() })
	z := zip.NewWriter(f)
	for name, data := range files {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(data)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return f
}
func dataFiles() map[string]string {
	return map[string]string{"pack.mcmeta": `{"pack":{"pack_format":1,"description":"PVZCE distribution test"}}`, "data/example/plants/test.json": `{"health":300,"capabilities":[]}`}
}
func TestVerifiedPVZCEFormats(t *testing.T) {
	f := archive(t, dataFiles())
	m, e := ValidatePackage(f, "datapack", "1.0.0", []string{"1.0.0"})
	if e != nil || m.PackFormat != 1 || !strings.Contains(m.Install, "Extract") {
		t.Fatalf("directory distribution: %+v %v", m, e)
	}
	mod := `{"schemaVersion":1,"id":"example_mod","version":"1.0.0","name":"Example Mod","description":"PVZCE empty demo mod template.","authors":["PVZCE"],"license":"MIT","environment":"*","depends":{"pvzce":"*"}}`
	f = archive(t, map[string]string{"fabric.mod.json": mod})
	m, e = ValidatePackage(f, "mod", "1.0.0", []string{"1.0.0"})
	if e != nil || m.ID != "example_mod" {
		t.Fatalf("real template metadata: %+v %v", m, e)
	}
	for name, files := range map[string]map[string]string{
		"minecraft":        {"fabric.mod.json": strings.Replace(mod, `"pvzce":"*"`, `"minecraft":"*"`, 1)},
		"version-mismatch": {"fabric.mod.json": strings.Replace(mod, `"version":"1.0.0"`, `"version":"2.0.0"`, 1)},
		"wrong-game-range": {"fabric.mod.json": strings.Replace(mod, `"pvzce":"*"`, `"pvzce":">=2.0.0"`, 1)},
		"no-root":          {"wrapper/pack.mcmeta": `{}`},
		"path-traversal":   {"../escape": "bad"},
		"windows-path":     {"C:/escape": "bad"},
		"backslash":        {"data\\evil": "bad"},
		"duplicate-case":   {"A": "1", "a": "2"},
		"bad-data":         {"pack.mcmeta": dataFiles()["pack.mcmeta"], "data/example/plants/foo.json": "{"},
		"legacy-level":     {"pack.mcmeta": dataFiles()["pack.mcmeta"], "data/example/levels/foo.json": `{"conveyor":{}}`},
		"code-in-datapack": {"pack.mcmeta": dataFiles()["pack.mcmeta"], "evil.class": "code"},
		"zip-bomb":         {"pack.mcmeta": dataFiles()["pack.mcmeta"], "data/example/plants/large.json": strings.Repeat("0", 2<<20)},
	} {
		t.Run(name, func(t *testing.T) {
			kind := "datapack"
			if strings.HasPrefix(name, "minecraft") || name == "version-mismatch" || name == "wrong-game-range" {
				kind = "mod"
			}
			if _, e := ValidatePackage(archive(t, files), kind, "1.0.0", []string{"1.0.0"}); e == nil {
				t.Fatal("unsafe/incompatible archive accepted")
			}
		})
	}
}
func TestSymlinkAndEntryLimits(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "link.zip")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0777)
	w, e := z.CreateHeader(h)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = w.Write([]byte("/etc/passwd"))
	_ = z.Close()
	if _, e := ValidatePackage(f, "datapack", "1.0.0", []string{"1.0.0"}); e == nil {
		t.Fatal("symlink accepted")
	}
	files := map[string]string{}
	for i := 0; i <= MaxArchiveEntries; i++ {
		files[strings.Repeat("x", i/1000)+string(rune(0x1000+i))] = ""
	}
	if _, e := ValidatePackage(archive(t, files), "datapack", "1.0.0", []string{"1.0.0"}); e == nil {
		t.Fatal("entry limit bypassed")
	}
}
func TestFabricPredicateSubset(t *testing.T) {
	for _, tc := range []struct {
		predicate string
		want      bool
	}{{`"*"`, true}, {`">=1.0.0 <2.0.0"`, true}, {`[">=2.0.0","1.x"]`, true}, {`"~1.0.0"`, true}, {`"^1.0.0"`, true}, {`"<1.0.0"`, false}, {`"1.1.x"`, false}, {`"nonsense"`, false}, {`""`, false}, {`"2.x"`, false}} {
		if got := matchesPredicate(json.RawMessage(tc.predicate), "1.0.0"); got != tc.want {
			t.Errorf("%s: got %v", tc.predicate, got)
		}
	}
}
func TestIconValidation(t *testing.T) {
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); e != nil {
		t.Fatal(e)
	}
	f, e := os.CreateTemp(t.TempDir(), "icon")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	_, _ = f.Write(b.Bytes())
	mime, e := ValidateIcon(f)
	if e != nil || mime != "image/png" {
		t.Fatal(mime, e)
	}
	if e = f.Truncate(10); e != nil {
		t.Fatal(e)
	}
	if _, e = ValidateIcon(f); e == nil {
		t.Fatal("truncated icon accepted")
	}
}
