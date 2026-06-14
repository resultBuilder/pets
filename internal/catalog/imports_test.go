package catalog

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"codex-pets/internal/protocol"
)

func testSpritePNG(t *testing.T) []byte {
	t.Helper()
	atlas := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			atlas.SetNRGBA(x, y, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, atlas); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportPetCanonicalizesAndValidates(t *testing.T) {
	root := t.TempDir()
	manifest := []byte(`{"slug": "boba-2", "displayName": "Boba!", "description": "otter", "frameWidth": 4, "frameHeight": 3, "license": "MIT"}`)

	ref, err := ImportPet(ImportRequest{
		Root:           root,
		Source:         "petdex",
		PetJSON:        manifest,
		Spritesheet:    testSpritePNG(t),
		SpritesheetExt: ".PNG",
	}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// Slug derives from the display name, not the legacy slug.
	wantDir := filepath.Join(root, "boba")
	if ref.Path != wantDir || ref.ID != "petdex:boba:"+wantDir {
		t.Fatalf("imported ref = %+v, want canonical boba in %s", ref, wantDir)
	}
	stored, err := os.ReadFile(filepath.Join(wantDir, "pet.json"))
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip map[string]any
	if err := json.Unmarshal(stored, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip["slug"] != "boba" || roundtrip["spritesheetPath"] != "spritesheet.png" {
		t.Fatalf("manifest was not canonicalized: %v", roundtrip)
	}

	// Re-import replaces the package instead of duplicating it.
	if _, err := ImportPet(ImportRequest{Root: root, PetJSON: manifest, Spritesheet: testSpritePNG(t), SpritesheetExt: "png"}, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("import root entries = %d, want 1", len(entries))
	}

	// Mismatched bytes are rejected before anything lands in the root.
	if _, err := ImportPet(ImportRequest{Root: root, PetJSON: manifest, Spritesheet: []byte("not an image"), SpritesheetExt: "png"}, DefaultLimits()); err == nil {
		t.Fatal("expected mismatched spritesheet bytes to be rejected")
	}
	if _, err := ImportPet(ImportRequest{Root: root, PetJSON: []byte(`{"displayName": ""}`), Spritesheet: testSpritePNG(t), SpritesheetExt: "png"}, DefaultLimits()); err == nil {
		t.Fatal("expected unnameable pet to be rejected")
	}
}

func TestUninstallPetStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "boba")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	if err := UninstallPet(root, outside); err == nil {
		t.Fatal("expected uninstall outside the root to fail")
	}
	if err := UninstallPet(root, root); err == nil {
		t.Fatal("expected uninstalling the root itself to fail")
	}
	if err := UninstallPet(root, filepath.Join(root, "boba", "nested")); err == nil {
		t.Fatal("expected non-direct children to be rejected")
	}
	if err := UninstallPet(root, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("pet directory should be removed")
	}
}

func TestDedupeInstalledPrefersCanonicalPackage(t *testing.T) {
	canonical := protocol.PetRef{ID: "petdex:boba:/pets/boba", DisplayName: "Boba", Path: "/pets/boba"}
	legacy := protocol.PetRef{ID: "petdex:boba-2:/old/boba-import", DisplayName: "Boba", Path: "/old/boba-import"}
	wrongDir := protocol.PetRef{ID: "app:pepe:/bundle/pets/misc", DisplayName: "Pepe", Path: "/bundle/pets/misc"}
	other := protocol.PetRef{ID: "app:cappy:/bundle/pets/cappy", DisplayName: "Cappy", Path: "/bundle/pets/cappy"}

	out := DedupeInstalled([]protocol.PetRef{legacy, canonical, wrongDir, other})
	if len(out) != 3 {
		t.Fatalf("deduped length = %d, want 3: %+v", len(out), out)
	}
	if out[0].ID != canonical.ID {
		t.Fatalf("canonical package should win, got %+v", out[0])
	}
	// Input order of first appearance is preserved.
	if out[1].ID != wrongDir.ID || out[2].ID != other.ID {
		t.Fatalf("unexpected order: %+v", out)
	}

	// Same rank keeps the first occurrence.
	first := protocol.PetRef{ID: "app:boba:/a/boba", DisplayName: "Boba", Path: "/a/boba"}
	second := protocol.PetRef{ID: "app:boba:/b/boba", DisplayName: "Boba", Path: "/b/boba"}
	if got := DedupeInstalled([]protocol.PetRef{first, second}); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("tie should keep the first package: %+v", got)
	}
}

func TestImportPetFromDirectory(t *testing.T) {
	source := t.TempDir()
	root := t.TempDir()
	dir := writeInstalledPet(t, source, "boba-folder", "boba-legacy", "Boba")

	ref, err := ImportPetFromDirectory(dir, root, "app", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(root, "boba")
	if ref.Path != wantDir || ref.ID != "app:boba:"+wantDir {
		t.Fatalf("imported ref = %+v, want canonical boba in %s", ref, wantDir)
	}
	if ref.Slug != "boba" || ref.SpritesheetPath != "spritesheet.png" || ref.FrameWidth != 4 || ref.FrameHeight != 3 {
		t.Fatalf("ref is not fully enriched: %+v", ref)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "spritesheet.png")); err != nil {
		t.Fatalf("sprite was not copied: %v", err)
	}

	if _, err := ImportPetFromDirectory(filepath.Join(source, "missing"), root, "app", DefaultLimits()); err == nil {
		t.Fatal("expected missing directory to fail")
	}
}

func TestBuildBrowserRowsMergesAndHidesDuplicates(t *testing.T) {
	importRoot := "/imports"
	installed := []protocol.PetRef{
		{ID: "app:boba:/imports/boba", Slug: "boba", DisplayName: "Boba", Source: "app", Path: "/imports/boba"},
		{ID: "petdex:pepe:/elsewhere/pepe", Slug: "pepe", DisplayName: "Pepe", Source: "petdex", Path: "/elsewhere/pepe"},
	}
	catalogPets := []protocol.PetRef{
		{ID: "prebundled:boba:/bundle/pets/boba", Slug: "boba", DisplayName: "Boba", Source: "prebundled", Path: "/bundle/pets/boba"},
		{ID: "prebundled:cappy:/bundle/pets/cappy", Slug: "cappy", DisplayName: "Cappy", Source: "prebundled", Path: "/bundle/pets/cappy"},
		{ID: "prebundled:pepe-two:/bundle/pets/pepe-two", Slug: "pepe-two", DisplayName: "Pepe", Source: "prebundled", Path: "/bundle/pets/pepe-two"},
	}

	rows := BuildBrowserRows(installed, catalogPets, importRoot)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (boba dup by slug and pepe dup by name hidden): %+v", len(rows), rows)
	}
	if !rows[0].Installed || !rows[0].CanUninstall {
		t.Fatalf("imported boba should be installed and uninstallable: %+v", rows[0])
	}
	if !rows[1].Installed || rows[1].CanUninstall {
		t.Fatalf("pepe outside the import root must not be uninstallable: %+v", rows[1])
	}
	if rows[2].Installed || rows[2].Slug != "cappy" {
		t.Fatalf("only cappy should remain from the catalog: %+v", rows[2])
	}
}
