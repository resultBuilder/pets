package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codex-pets/internal/protocol"
)

// ImportRequest is a validated pet package write into Root. The daemon owns
// this flow so every platform applies identical canonicalization, size, and
// MIME rules.
type ImportRequest struct {
	Root           string
	Source         string
	PetJSON        []byte
	Spritesheet    []byte
	SpritesheetExt string
}

// ImportPet canonicalizes the manifest (slug derived from the display name,
// spritesheet path rewritten to the stored file) and writes the package
// atomically to Root/<slug>, replacing any previous import of the same pet.
func ImportPet(req ImportRequest, limits Limits) (protocol.PetRef, error) {
	if limits.MaxPetJSONBytes == 0 {
		limits = DefaultLimits()
	}
	if req.Root == "" {
		return protocol.PetRef{}, errors.New("pet import root is not configured")
	}
	if int64(len(req.PetJSON)) > limits.MaxPetJSONBytes {
		return protocol.PetRef{}, errors.New("pet.json exceeds size limit")
	}
	if int64(len(req.Spritesheet)) > limits.MaxSpriteBytes {
		return protocol.PetRef{}, errors.New("spritesheet exceeds size limit")
	}

	var manifest map[string]any
	if err := json.Unmarshal(req.PetJSON, &manifest); err != nil {
		return protocol.PetRef{}, fmt.Errorf("pet.json could not be parsed: %w", err)
	}

	slug := Slugify(firstNonEmpty(
		stringValue(manifest["displayName"]),
		stringValue(manifest["name"]),
		stringValue(manifest["slug"]),
		stringValue(manifest["id"]),
	))
	if slug == "" {
		return protocol.PetRef{}, errors.New("pet has no usable name to derive a slug from")
	}

	ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.SpritesheetExt), "."))
	if ext != "png" && ext != "webp" {
		return protocol.PetRef{}, fmt.Errorf("unsupported spritesheet extension %q", req.SpritesheetExt)
	}
	spriteName := "spritesheet." + ext
	if detectSpriteMIME("."+ext, req.Spritesheet) == "" {
		return protocol.PetRef{}, errors.New("spritesheet bytes do not match the declared format")
	}

	manifest["slug"] = slug
	manifest["spritesheetPath"] = spriteName
	canonicalJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return protocol.PetRef{}, err
	}

	if err := os.MkdirAll(req.Root, 0o755); err != nil {
		return protocol.PetRef{}, err
	}
	staging, err := os.MkdirTemp(req.Root, ".import-"+slug+"-")
	if err != nil {
		return protocol.PetRef{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o755); err != nil {
		return protocol.PetRef{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "pet.json"), canonicalJSON, 0o644); err != nil {
		return protocol.PetRef{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, spriteName), req.Spritesheet, 0o644); err != nil {
		return protocol.PetRef{}, err
	}

	// The staged package must load through the same validation as scanning,
	// so a broken import can never enter the pets directory.
	if _, err := LoadLocalPet(staging, limits); err != nil {
		return protocol.PetRef{}, fmt.Errorf("imported pet failed validation: %w", err)
	}

	destination := filepath.Join(req.Root, slug)
	if err := os.RemoveAll(destination); err != nil {
		return protocol.PetRef{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return protocol.PetRef{}, err
	}

	pet, err := LoadLocalPet(destination, limits)
	if err != nil {
		return protocol.PetRef{}, err
	}
	return installedPetRef(pet, firstNonEmpty(req.Source, "petdex"), destination), nil
}

// ImportPetFromDirectory imports a local pet package (a bundled catalog pet
// or a user-picked folder) through the same validation as byte imports.
func ImportPetFromDirectory(dir string, root string, source string, limits Limits) (protocol.PetRef, error) {
	if limits.MaxPetJSONBytes == 0 {
		limits = DefaultLimits()
	}
	pet, err := LoadLocalPet(dir, limits)
	if err != nil {
		return protocol.PetRef{}, err
	}
	manifest, err := readLimitedFile(filepath.Join(dir, "pet.json"), limits.MaxPetJSONBytes)
	if err != nil {
		return protocol.PetRef{}, err
	}
	sprite, err := readLimitedFile(pet.SourcePath, limits.MaxSpriteBytes)
	if err != nil {
		return protocol.PetRef{}, err
	}
	return ImportPet(ImportRequest{
		Root:           root,
		Source:         source,
		PetJSON:        manifest,
		Spritesheet:    sprite,
		SpritesheetExt: filepath.Ext(pet.SourcePath),
	}, limits)
}

// UninstallPet removes an imported pet directory. It refuses anything that
// does not resolve to a direct child of root.
func UninstallPet(root string, petDir string) error {
	if root == "" {
		return errors.New("pet import root is not configured")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(petDir)
	if err != nil {
		return err
	}
	if filepath.Dir(absDir) != absRoot || absDir == absRoot {
		return fmt.Errorf("pet directory %q is outside the import root", petDir)
	}
	return os.RemoveAll(absDir)
}
