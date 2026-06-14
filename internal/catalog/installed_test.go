package catalog

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writeInstalledPet(t *testing.T, root string, dirName string, slug string, displayName string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	atlas := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			atlas.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	file, err := os.Create(filepath.Join(dir, "spritesheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, atlas); err != nil {
		t.Fatal(err)
	}
	file.Close()
	manifest := `{"slug": "` + slug + `", "displayName": "` + displayName + `", "spritesheetPath": "spritesheet.png", "frameWidth": 4, "frameHeight": 3, "license": "MIT", "attribution": "tests"}`
	if err := os.WriteFile(filepath.Join(dir, "pet.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanInstalledPetsBuildsSharedRefs(t *testing.T) {
	appRoot := t.TempDir()
	userRoot := t.TempDir()
	bobaDir := writeInstalledPet(t, appRoot, "boba", "boba", "Boba")
	writeInstalledPet(t, userRoot, "zorro", "zorro", "Zorro")
	writeInstalledPet(t, appRoot, "aqua", "aqua", "Aqua Wisp")
	// Broken packages are skipped, not fatal.
	if err := os.MkdirAll(filepath.Join(appRoot, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}

	refs := ScanInstalledPets([]InstalledRoot{
		{Dir: appRoot, Source: "app"},
		{Dir: userRoot, Source: "petdex"},
		{Dir: filepath.Join(appRoot, "does-not-exist"), Source: "app"},
	}, DefaultLimits())

	if len(refs) != 3 {
		t.Fatalf("scanned refs = %d, want 3: %+v", len(refs), refs)
	}
	if refs[0].DisplayName != "Aqua Wisp" || refs[1].DisplayName != "Boba" || refs[2].DisplayName != "Zorro" {
		t.Fatalf("refs are not sorted by display name: %+v", refs)
	}
	boba := refs[1]
	if boba.ID != "app:boba:"+bobaDir {
		t.Fatalf("pet id = %q, want macOS-compatible app:boba:%s", boba.ID, bobaDir)
	}
	if boba.Path != bobaDir || boba.Source != "app" || boba.License != "MIT" {
		t.Fatalf("unexpected ref fields: %+v", boba)
	}
}
