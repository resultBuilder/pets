package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePetdexManifestSupportsV1AndV2(t *testing.T) {
	v1 := []byte(`{
		"pets": [{
			"slug": "Boba Cat",
			"displayName": "Boba Cat",
			"description": "Round friend",
			"kind": "cat",
			"submittedBy": "petdex",
			"spritesheetUrl": "/pets/boba/spritesheet.webp",
			"petJsonUrl": "/pets/boba/pet.json",
			"license": "CC-BY",
			"tags": ["soft", "Soft", "round"]
		}]
	}`)
	pets, err := ParsePetdexManifest(v1)
	if err != nil {
		t.Fatalf("parse v1: %v", err)
	}
	if len(pets) != 1 {
		t.Fatalf("pets = %d, want 1", len(pets))
	}
	if pets[0].ID != "boba-cat" {
		t.Fatalf("id = %q", pets[0].ID)
	}
	if pets[0].SpritesheetURL != "https://assets.petdex.dev/pets/boba/spritesheet.webp" {
		t.Fatalf("spritesheet = %q", pets[0].SpritesheetURL)
	}
	if len(pets[0].Tags) != 2 {
		t.Fatalf("tags = %+v", pets[0].Tags)
	}

	v2 := []byte(`{
		"v": 2,
		"assetBase": "https://cdn.example.test",
		"pets": [["miso", "Miso", "fox", "ana", "/miso.webp", "/miso.json", "/miso.zip"]]
	}`)
	pets, err = ParsePetdexManifest(v2)
	if err != nil {
		t.Fatalf("parse v2: %v", err)
	}
	if pets[0].PetJSONURL != "https://cdn.example.test/miso.json" {
		t.Fatalf("pet json = %q", pets[0].PetJSONURL)
	}
	if pets[0].PackageURL != "https://cdn.example.test/miso.zip" {
		t.Fatalf("package = %q", pets[0].PackageURL)
	}
}

func TestLoadLocalPetValidatesManifestAndRasterSprite(t *testing.T) {
	root := t.TempDir()
	petDir := filepath.Join(root, ".codex", "pets", "miso")
	if err := os.MkdirAll(petDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(petDir, "pet.json"), []byte(`{
		"slug": "miso",
		"displayName": "Miso",
		"spritesheetPath": "spritesheet.png",
		"frameWidth": 96,
		"frameHeight": 104,
		"license": "MIT",
		"attribution": "local artist"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(filepath.Join(petDir, "spritesheet.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	pet, err := LoadLocalPet(petDir, DefaultLimits())
	if err != nil {
		t.Fatalf("load local pet: %v", err)
	}
	if pet.Provider != "codex-local" {
		t.Fatalf("provider = %q", pet.Provider)
	}
	if pet.FrameWidth != 96 || pet.FrameHeight != 104 {
		t.Fatalf("frame = %dx%d", pet.FrameWidth, pet.FrameHeight)
	}
	if pet.License != "MIT" || pet.Attribution != "local artist" {
		t.Fatalf("license/attribution missing: %+v", pet)
	}
}

func TestLoadLocalPetRejectsWrongMimeAndOversizedJSON(t *testing.T) {
	root := t.TempDir()
	petDir := filepath.Join(root, "bad")
	if err := os.MkdirAll(petDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(petDir, "pet.json"), []byte(`{"slug":"bad","spritesheetPath":"spritesheet.png"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(petDir, "spritesheet.png"), []byte("not png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalPet(petDir, DefaultLimits()); err == nil {
		t.Fatal("expected wrong mime to fail")
	}

	oversizedDir := filepath.Join(root, "oversized")
	if err := os.MkdirAll(oversizedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oversizedDir, "pet.json"), []byte(`{"slug":"oversized"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalPet(oversizedDir, Limits{MaxPetJSONBytes: 4, MaxSpriteBytes: DefaultMaxSpriteBytes}); err == nil {
		t.Fatal("expected oversized pet.json to fail")
	}
}

func TestLocalProviderScansCodexAndPetdexRoots(t *testing.T) {
	root := t.TempDir()
	codexRoot := filepath.Join(root, ".codex", "pets")
	petdexRoot := filepath.Join(root, ".petdex", "pets")
	for _, dir := range []string{filepath.Join(codexRoot, "a"), filepath.Join(petdexRoot, "b")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pet.json"), []byte(`{"slug":"`+filepath.Base(dir)+`","displayName":"`+filepath.Base(dir)+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), byte(0))
		if err := os.WriteFile(filepath.Join(dir, "spritesheet.webp"), webp, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	provider := LocalProvider{Roots: []string{codexRoot, petdexRoot}}
	pets, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("list local: %v", err)
	}
	if len(pets) != 2 {
		t.Fatalf("pets = %d, want 2", len(pets))
	}
}

func TestPadXProviderIsDocumentedStub(t *testing.T) {
	_, err := PadXProvider{}.List(context.Background())
	if !errors.Is(err, ErrPadXUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if PadXStubNote() == "" {
		t.Fatal("stub note must be documented")
	}
}
