package workshop

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const MaxExpandedBytes int64 = 512 << 20
const MaxArchiveEntries = 10000
const MaxEntryBytes int64 = 64 << 20

var modID = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
var namespace = regexp.MustCompile(`^[a-z0-9_.-]+$`)
var dataPath = regexp.MustCompile(`^[a-z0-9_./-]+$`)
var semver = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
var registries = map[string]bool{"attributes": true, "levels": true, "level_themes": true, "level_categories": true, "endless_schedules": true, "dialogue_characters": true, "damage_types": true, "plants": true, "zombies": true, "projectiles": true, "resources": true, "slots": true, "tools": true, "scene_elements": true, "liquids": true, "sound_events": true, "particles": true, "tags": true}

type PackageMetadata struct {
	Format        string                     `json:"format"`
	ID            string                     `json:"id,omitempty"`
	Version       string                     `json:"version,omitempty"`
	Depends       map[string]json.RawMessage `json:"depends,omitempty"`
	Breaks        map[string]json.RawMessage `json:"breaks,omitempty"`
	PackFormat    int                        `json:"pack_format,omitempty"`
	Entries       int                        `json:"entries"`
	ExpandedBytes int64                      `json:"expanded_bytes"`
	Install       string                     `json:"install"`
}

// ValidatePackage reads bounded entry streams without extracting, loading classes, or executing code.
// This checks the verified PVZCE distribution contract, not the Java codecs' complete gameplay semantics.
func ValidatePackage(file *os.File, kind, number string, gameVersions []string) (PackageMetadata, error) {
	var out PackageMetadata
	info, e := file.Stat()
	if e != nil {
		return out, e
	}
	z, e := zip.NewReader(file, info.Size())
	if e != nil {
		return out, fmt.Errorf("%w: expected a ZIP/JAR", ErrInvalid)
	}
	if len(z.File) == 0 || len(z.File) > MaxArchiveEntries {
		return out, fmt.Errorf("%w: archive entry limit", ErrInvalid)
	}
	seen := map[string]bool{}
	metadata := map[string][]byte{}
	content := 0
	for _, f := range z.File {
		name := f.Name
		clean := strings.TrimSuffix(name, "/")
		if len(name) > 500 || !fs.ValidPath(clean) || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(clean) != clean || seen[strings.ToLower(clean)] {
			return out, fmt.Errorf("%w: unsafe or duplicate archive path", ErrInvalid)
		}
		seen[strings.ToLower(clean)] = true
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) || f.Flags&1 != 0 {
			return out, fmt.Errorf("%w: special/encrypted entry", ErrInvalid)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > uint64(MaxEntryBytes) || f.UncompressedSize64 > uint64(MaxExpandedBytes-out.ExpandedBytes) || (f.UncompressedSize64 > 1<<20 && f.UncompressedSize64 > f.CompressedSize64*200) {
			return out, fmt.Errorf("%w: archive expansion limit", ErrInvalid)
		}
		out.ExpandedBytes += int64(f.UncompressedSize64)
		out.Entries++
		r, e := f.Open()
		if e != nil {
			return out, ErrInvalid
		}
		isMeta := name == "fabric.mod.json" || name == "pack.mcmeta"
		isData := strings.HasPrefix(name, "data/") && strings.HasSuffix(name, ".json")
		var data []byte
		if isMeta || isData {
			data, e = io.ReadAll(io.LimitReader(r, 2<<20+1))
			if len(data) > 2<<20 {
				e = ErrInvalid
			}
		} else {
			var n int64
			n, e = io.Copy(io.Discard, io.LimitReader(r, MaxEntryBytes+1))
			if n > MaxEntryBytes {
				e = ErrInvalid
			}
		}
		ce := r.Close()
		if e != nil || ce != nil {
			return out, fmt.Errorf("%w: corrupt or oversized archive entry", ErrInvalid)
		}
		if isMeta {
			metadata[name] = data
		}
		if isData {
			parts := strings.Split(name, "/")
			if len(parts) < 4 || !namespace.MatchString(parts[1]) || !registries[parts[2]] || !dataPath.MatchString(strings.Join(parts[3:], "/")) {
				return out, fmt.Errorf("%w: invalid PVZCE data path", ErrInvalid)
			}
			var obj map[string]json.RawMessage
			if json.Unmarshal(data, &obj) != nil || obj == nil {
				return out, fmt.Errorf("%w: invalid data JSON", ErrInvalid)
			}
			if parts[2] == "levels" && (obj["conveyor"] != nil || obj["placement_zone"] != nil) {
				return out, fmt.Errorf("%w: legacy level keys must move into mechanics", ErrInvalid)
			}
			content++
		}
		if kind == "datapack" && (strings.HasSuffix(strings.ToLower(name), ".class") || strings.HasSuffix(strings.ToLower(name), ".jar") || strings.HasSuffix(strings.ToLower(name), ".zip")) {
			return out, fmt.Errorf("%w: data packs cannot contain code or nested archives", ErrInvalid)
		}
	}
	if kind == "mod" {
		var m struct {
			Schema      int                        `json:"schemaVersion"`
			ID          string                     `json:"id"`
			Version     string                     `json:"version"`
			Environment string                     `json:"environment"`
			Depends     map[string]json.RawMessage `json:"depends"`
			Breaks      map[string]json.RawMessage `json:"breaks"`
			Jars        []json.RawMessage          `json:"jars"`
		}
		if json.Unmarshal(metadata["fabric.mod.json"], &m) != nil || m.Schema != 1 || !modID.MatchString(m.ID) || m.Version != number || !semver.MatchString(m.Version) || !oneOf(m.Environment, "", "*", "client", "server") || len(m.Jars) > 0 {
			return out, fmt.Errorf("%w: invalid/unsupported PVZCE mod metadata", ErrInvalid)
		}
		if m.Depends["minecraft"] != nil {
			return out, fmt.Errorf("%w: Minecraft mods are not PVZCE mods", ErrInvalid)
		}
		raw := m.Depends["pvzce"]
		if raw == nil {
			return out, fmt.Errorf("%w: depends.pvzce is required", ErrInvalid)
		}
		for _, v := range gameVersions {
			if !matchesPredicate(raw, v) {
				return out, fmt.Errorf("%w: game version does not satisfy depends.pvzce", ErrInvalid)
			}
		}
		out.Format = "pvzce-fabric-schema-1"
		out.ID = m.ID
		out.Version = m.Version
		out.Depends = m.Depends
		out.Breaks = m.Breaks
		out.Install = "Place the JAR in mods/. No remap or mixin support is assumed."
	} else if kind == "datapack" {
		var m struct {
			Pack struct {
				Format      int    `json:"pack_format"`
				Description string `json:"description"`
			} `json:"pack"`
		}
		if metadata["fabric.mod.json"] != nil || json.Unmarshal(metadata["pack.mcmeta"], &m) != nil || m.Pack.Format != 1 || m.Pack.Description == "" || content == 0 {
			return out, fmt.Errorf("%w: require root pack.mcmeta (PVZCE format 1), description and data/<namespace>/<registry>/*.json", ErrInvalid)
		}
		out.Format = "pvzce-directory-distribution"
		out.PackFormat = m.Pack.Format
		out.Install = "Extract ZIP contents into datapacks/<pack>/, then /reload. The game does not load ZIP files directly."
	} else {
		return out, ErrInvalid
	}
	return out, nil
}
func matchesPredicate(raw []byte, version string) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return matchString(text, version)
	}
	var alternatives []string
	if json.Unmarshal(raw, &alternatives) != nil || len(alternatives) == 0 {
		return false
	}
	for _, p := range alternatives {
		if matchString(p, version) {
			return true
		}
	}
	return false
}
func versionParts(v string) ([3]int, bool) {
	var out [3]int
	m := semver.FindStringSubmatch(v)
	if m == nil {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, e := strconv.Atoi(m[i+1])
		if e != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
func compare(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
func matchString(predicate, version string) bool {
	v, ok := versionParts(version)
	if !ok {
		return false
	}
	parts := strings.Fields(predicate)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if p == "*" {
			continue
		}
		op := "="
		for _, candidate := range []string{">=", "<=", ">", "<", "=", "~", "^"} {
			if strings.HasPrefix(p, candidate) {
				op = candidate
				p = strings.TrimPrefix(p, candidate)
				break
			}
		}
		if strings.ContainsAny(p, "xX*") {
			if op != "=" {
				return false
			}
			ps := strings.Split(p, ".")
			if len(ps) > 3 {
				return false
			}
			for i, s := range ps {
				if oneOf(s, "x", "X", "*") {
					break
				}
				n, e := strconv.Atoi(s)
				if e != nil || n != v[i] {
					return false
				}
			}
			continue
		}
		target, ok := versionParts(p)
		if !ok || strings.Contains(p, "-") {
			return false
		}
		cmp := compare(v, target)
		switch op {
		case "=":
			if cmp != 0 {
				return false
			}
		case ">=":
			if cmp < 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case "~":
			if cmp < 0 || v[0] != target[0] || v[1] != target[1] {
				return false
			}
		case "^":
			if cmp < 0 || v[0] != target[0] || (target[0] == 0 && v[1] != target[1]) || (target[0] == 0 && target[1] == 0 && v[2] != target[2]) {
				return false
			}
		}
	}
	return true
}
func ValidateIcon(f *os.File) (string, error) {
	if _, e := f.Seek(0, 0); e != nil {
		return "", e
	}
	cfg, format, e := image.DecodeConfig(f)
	if e != nil || !oneOf(format, "png", "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
		return "", ErrInvalid
	}
	if _, e = f.Seek(0, 0); e != nil {
		return "", e
	}
	_, format, e = image.Decode(f)
	if e != nil {
		return "", ErrInvalid
	}
	if format == "jpeg" {
		return "image/jpeg", nil
	}
	return "image/png", nil
}
