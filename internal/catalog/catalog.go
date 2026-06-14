package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultMaxPetJSONBytes = 256 * 1024
	DefaultMaxSpriteBytes  = 10 * 1024 * 1024
	DefaultAssetBase       = "https://assets.petdex.dev"
)

var ErrPadXUnavailable = errors.New("padx pet catalog API/package format is not configured or discoverable")

type Provider interface {
	Name() string
	List(ctx context.Context) ([]Pet, error)
}

type Pet struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	DisplayName    string   `json:"displayName"`
	Description    string   `json:"description,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	SpritesheetURL string   `json:"spritesheetUrl,omitempty"`
	PetJSONURL     string   `json:"petJsonUrl,omitempty"`
	PackageURL     string   `json:"packageUrl,omitempty"`
	FrameWidth     int      `json:"frameWidth"`
	FrameHeight    int      `json:"frameHeight"`
	License        string   `json:"license,omitempty"`
	Attribution    string   `json:"attribution,omitempty"`
	SourcePath     string   `json:"sourcePath,omitempty"`
}

type Limits struct {
	MaxPetJSONBytes int64
	MaxSpriteBytes  int64
}

func DefaultLimits() Limits {
	return Limits{
		MaxPetJSONBytes: DefaultMaxPetJSONBytes,
		MaxSpriteBytes:  DefaultMaxSpriteBytes,
	}
}

func NormalizePet(input Pet) (Pet, error) {
	pet := input
	pet.ID = Slugify(firstNonEmpty(pet.ID, pet.DisplayName))
	pet.Provider = clamp(firstNonEmpty(pet.Provider, "unknown"), 80)
	pet.DisplayName = clamp(firstNonEmpty(pet.DisplayName, pet.ID), 160)
	pet.Description = clamp(pet.Description, 500)
	pet.Kind = clamp(firstNonEmpty(pet.Kind, "pet"), 80)
	pet.SpritesheetURL = strings.TrimSpace(pet.SpritesheetURL)
	pet.PetJSONURL = strings.TrimSpace(pet.PetJSONURL)
	pet.PackageURL = strings.TrimSpace(pet.PackageURL)
	pet.License = clamp(pet.License, 120)
	pet.Attribution = clamp(pet.Attribution, 240)
	pet.SourcePath = clamp(pet.SourcePath, 500)
	pet.Tags = uniqueStrings(pet.Tags, 20, 60)
	if pet.FrameWidth <= 0 {
		pet.FrameWidth = 192
	}
	if pet.FrameHeight <= 0 {
		pet.FrameHeight = 208
	}
	if pet.ID == "" {
		return Pet{}, errors.New("pet id is required")
	}
	if pet.SpritesheetURL == "" && pet.SourcePath == "" {
		return Pet{}, errors.New("pet spritesheet is required")
	}
	return pet, nil
}

func ParsePetdexManifest(data []byte) ([]Pet, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	base := stringValue(root["assetBase"])
	if base == "" {
		base = DefaultAssetBase
	}

	rawPets, ok := root["pets"].([]any)
	if !ok {
		return nil, errors.New("manifest pets array is required")
	}

	var pets []Pet
	if intValue(root["v"]) == 2 {
		for _, item := range rawPets {
			values, ok := item.([]any)
			if !ok || len(values) < 5 {
				continue
			}
			pet, err := NormalizePet(Pet{
				ID:             stringValue(values[0]),
				Provider:       "petdex",
				DisplayName:    stringValue(values[1]),
				Kind:           stringValue(values[2]),
				Attribution:    stringValue(values[3]),
				SpritesheetURL: absolutizeAsset(stringValue(values[4]), base),
				PetJSONURL:     absolutizeAsset(stringValueAt(values, 5), base),
				PackageURL:     absolutizeAsset(stringValueAt(values, 6), base),
				FrameWidth:     192,
				FrameHeight:    208,
				License:        "unknown",
			})
			if err == nil {
				pets = append(pets, pet)
			}
		}
		return sortPets(pets), nil
	}

	for _, item := range rawPets {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pet, err := NormalizePet(Pet{
			ID:             firstNonEmpty(stringValue(object["slug"]), stringValue(object["id"])),
			Provider:       "petdex",
			DisplayName:    firstNonEmpty(stringValue(object["displayName"]), stringValue(object["name"])),
			Description:    stringValue(object["description"]),
			Kind:           stringValue(object["kind"]),
			Tags:           stringList(object["tags"]),
			SpritesheetURL: absolutizeAsset(firstNonEmpty(stringValue(object["spritesheetUrl"]), stringValue(object["spritesheetPath"]), stringValue(object["spritesheet"]), stringValue(object["spriteUrl"])), base),
			PetJSONURL:     absolutizeAsset(firstNonEmpty(stringValue(object["petJsonUrl"]), stringValue(object["petJSONUrl"]), stringValue(object["petJSONURL"])), base),
			PackageURL:     absolutizeAsset(firstNonEmpty(stringValue(object["zipUrl"]), stringValue(object["zipURL"]), stringValue(object["packageUrl"])), base),
			FrameWidth:     intValue(object["frameWidth"]),
			FrameHeight:    intValue(object["frameHeight"]),
			License:        firstNonEmpty(stringValue(object["license"]), "unknown"),
			Attribution:    firstNonEmpty(stringValue(object["submittedBy"]), stringValue(object["author"])),
		})
		if err == nil {
			pets = append(pets, pet)
		}
	}
	return sortPets(pets), nil
}

type LocalProvider struct {
	Roots  []string
	Limits Limits
}

func (p LocalProvider) Name() string {
	return "local"
}

func (p LocalProvider) List(_ context.Context) ([]Pet, error) {
	limits := p.Limits
	if limits.MaxPetJSONBytes == 0 {
		limits = DefaultLimits()
	}
	var pets []Pet
	for _, root := range p.Roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pet, err := LoadLocalPet(filepath.Join(root, entry.Name()), limits)
			if err == nil {
				pets = append(pets, pet)
			}
		}
	}
	return sortPets(pets), nil
}

func DefaultLocalRoots(home string) []string {
	return []string{
		filepath.Join(home, ".codex", "pets"),
		filepath.Join(home, ".petdex", "pets"),
	}
}

func LoadLocalPet(dir string, limits Limits) (Pet, error) {
	if limits.MaxPetJSONBytes == 0 {
		limits = DefaultLimits()
	}
	manifestPath := filepath.Join(dir, "pet.json")
	manifest, err := readLimitedFile(manifestPath, limits.MaxPetJSONBytes)
	if err != nil {
		return Pet{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(manifest, &raw); err != nil {
		return Pet{}, err
	}

	sprite, mime, err := findSpritesheet(dir, raw, limits)
	if err != nil {
		return Pet{}, err
	}
	if mime != "image/png" && mime != "image/webp" {
		return Pet{}, fmt.Errorf("unsupported spritesheet mime %q", mime)
	}

	return NormalizePet(Pet{
		ID:          firstNonEmpty(stringValue(raw["slug"]), stringValue(raw["id"]), filepath.Base(dir)),
		Provider:    localProviderForPath(dir),
		DisplayName: firstNonEmpty(stringValue(raw["displayName"]), stringValue(raw["name"]), filepath.Base(dir)),
		Description: stringValue(raw["description"]),
		Kind:        stringValue(raw["kind"]),
		Tags:        stringList(raw["tags"]),
		SourcePath:  sprite,
		FrameWidth:  intValue(raw["frameWidth"]),
		FrameHeight: intValue(raw["frameHeight"]),
		License:     firstNonEmpty(stringValue(raw["license"]), "unknown"),
		Attribution: stringValue(raw["attribution"]),
	})
}

type PadXProvider struct{}

func (PadXProvider) Name() string {
	return "padx"
}

func (PadXProvider) List(context.Context) ([]Pet, error) {
	return nil, ErrPadXUnavailable
}

func PadXStubNote() string {
	return "PadXProvider is intentionally registered behind the catalog Provider interface, but no public PadX pet catalog API or package format was discoverable. Provide a manifest URL, local package format, or SDK/API documentation to replace this stub."
}

func findSpritesheet(dir string, manifest map[string]any, limits Limits) (string, string, error) {
	names := []string{}
	if raw := firstNonEmpty(stringValue(manifest["spritesheetPath"]), stringValue(manifest["spritesheet"])); raw != "" {
		names = append(names, raw)
	}
	names = append(names, "spritesheet.webp", "spritesheet.png", "sprite.webp", "sprite.png")

	for _, name := range names {
		candidate := filepath.Clean(filepath.Join(dir, name))
		rel, err := filepath.Rel(dir, candidate)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		data, err := readLimitedFile(candidate, limits.MaxSpriteBytes)
		if err != nil {
			continue
		}
		mime := detectSpriteMIME(filepath.Ext(candidate), data)
		if mime == "image/png" || mime == "image/webp" {
			return candidate, mime, nil
		}
	}
	return "", "", errors.New("compatible spritesheet was not found")
}

func readLimitedFile(filePath string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() > maxBytes {
		return nil, fmt.Errorf("%s exceeds size limit", filepath.Base(filePath))
	}
	return io.ReadAll(io.LimitReader(file, maxBytes+1))
}

func detectSpriteMIME(ext string, data []byte) string {
	ext = strings.ToLower(ext)
	if ext == ".png" && len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if ext == ".webp" && len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

func absolutizeAsset(value string, base string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return value
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(value, "/")
}

func sortPets(pets []Pet) []Pet {
	sort.Slice(pets, func(i, j int) bool {
		return strings.ToLower(pets[i].DisplayName) < strings.ToLower(pets[j].DisplayName)
	})
	return pets
}

func localProviderForPath(dir string) string {
	if strings.Contains(dir, string(filepath.Separator)+".codex"+string(filepath.Separator)) {
		return "codex-local"
	}
	if strings.Contains(dir, string(filepath.Separator)+".petdex"+string(filepath.Separator)) {
		return "petdex-local"
	}
	return "local"
}

func Slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func stringValueAt(values []any, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return stringValue(values[index])
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed
		}
	case float64:
		if typed > 0 {
			return int(typed)
		}
	case string:
		var parsed int
		_, _ = fmt.Sscanf(typed, "%d", &parsed)
		if parsed > 0 {
			return parsed
		}
	}
	return 0
}

func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := stringValue(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func uniqueStrings(values []string, maxItems int, maxLength int) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		value = clamp(value, maxLength)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func clamp(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	return value[:max]
}
