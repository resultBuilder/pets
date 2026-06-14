package catalog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codex-pets/internal/protocol"
)

// InstalledRoot is a directory whose immediate subdirectories are pet
// packages (pet.json + spritesheet). Source labels the origin the same way
// the macOS app does ("app", "petdex", "codex"), so pet IDs stay comparable
// across platforms.
type InstalledRoot struct {
	Dir    string
	Source string
}

// ScanInstalledPets loads every valid pet package under the given roots and
// returns wire-level refs ordered by display name. IDs follow the shared
// "<source>:<slug>:<directory>" scheme used by the macOS host.
func ScanInstalledPets(roots []InstalledRoot, limits Limits) []protocol.PetRef {
	var refs []protocol.PetRef
	seen := map[string]struct{}{}
	for _, root := range roots {
		entries, err := os.ReadDir(root.Dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root.Dir, entry.Name())
			pet, err := LoadLocalPet(dir, limits)
			if err != nil {
				continue
			}
			ref := installedPetRef(pet, root.Source, dir)
			if _, duplicate := seen[ref.ID]; duplicate {
				continue
			}
			seen[ref.ID] = struct{}{}
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		return strings.ToLower(refs[i].DisplayName) < strings.ToLower(refs[j].DisplayName)
	})
	return refs
}

// InstalledPetID mirrors PetPackage.id in the macOS app.
func InstalledPetID(source string, slug string, dir string) string {
	return source + ":" + slug + ":" + dir
}

// installedPetRef flattens a loaded pet package into the wire ref carrying
// everything hosts need to render it without parsing pet.json.
func installedPetRef(pet Pet, source string, dir string) protocol.PetRef {
	return protocol.PetRef{
		ID:              InstalledPetID(source, pet.ID, dir),
		Slug:            pet.ID,
		DisplayName:     pet.DisplayName,
		Description:     pet.Description,
		Kind:            pet.Kind,
		Source:          source,
		Path:            dir,
		SpritesheetPath: filepath.Base(pet.SourcePath),
		FrameWidth:      pet.FrameWidth,
		FrameHeight:     pet.FrameHeight,
		License:         pet.License,
		Attribution:     pet.Attribution,
	}
}

// BuildBrowserRows merges installed pets with catalog entries into the
// picker list: installed rows first, then catalog rows that do not
// duplicate an installed pet by slug or normalized display name. This is
// the single implementation of what web/native pickers used to compute.
func BuildBrowserRows(installed []protocol.PetRef, catalogPets []protocol.PetRef, importRoot string) []protocol.BrowserPetRow {
	rows := make([]protocol.BrowserPetRow, 0, len(installed)+len(catalogPets))
	slugs := map[string]struct{}{}
	names := map[string]struct{}{}
	for _, pet := range installed {
		slugs[slugFromID(pet.ID)] = struct{}{}
		names[normalizedPetName(pet)] = struct{}{}
		rows = append(rows, protocol.BrowserPetRow{
			PetRef:       pet,
			Installed:    true,
			CanUninstall: isDirectChild(importRoot, pet.Path),
		})
	}
	for _, pet := range catalogPets {
		slug := pet.Slug
		if slug == "" {
			slug = slugFromID(pet.ID)
		}
		if _, dup := slugs[slug]; dup {
			continue
		}
		if _, dup := names[normalizedPetName(pet)]; dup {
			continue
		}
		rows = append(rows, protocol.BrowserPetRow{PetRef: pet})
	}
	return rows
}

func isDirectChild(root string, path string) bool {
	if root == "" || path == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return filepath.Dir(absPath) == absRoot && absPath != absRoot
}

// DedupeInstalled collapses duplicate packages of the same pet (for example
// a legacy import with a non-canonical slug next to the canonical one). Pets
// are grouped by normalized display name and the best-ranked package wins;
// input order is preserved otherwise.
func DedupeInstalled(refs []protocol.PetRef) []protocol.PetRef {
	type slot struct {
		ref  protocol.PetRef
		rank int
	}
	byName := map[string]*slot{}
	order := []string{}
	for _, ref := range refs {
		key := normalizedPetName(ref)
		rank := installedPetRank(ref)
		existing, ok := byName[key]
		if !ok {
			byName[key] = &slot{ref: ref, rank: rank}
			order = append(order, key)
			continue
		}
		if rank < existing.rank {
			existing.ref = ref
			existing.rank = rank
		}
	}
	out := make([]protocol.PetRef, 0, len(order))
	for _, key := range order {
		out = append(out, byName[key].ref)
	}
	return out
}

// installedPetRank prefers packages whose slug and directory name match the
// canonical slug derived from the display name; lower is better.
func installedPetRank(ref protocol.PetRef) int {
	slug := slugFromID(ref.ID)
	canonical := Slugify(firstNonEmpty(ref.DisplayName, slug))
	rank := 0
	if slug != canonical {
		rank += 10
	}
	if filepath.Base(ref.Path) != canonical {
		rank++
	}
	return rank
}

func normalizedPetName(ref protocol.PetRef) string {
	name := firstNonEmpty(ref.DisplayName, slugFromID(ref.ID))
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// slugFromID extracts the slug from the shared "<source>:<slug>:<dir>" id.
func slugFromID(id string) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) >= 2 && parts[1] != "" {
		return parts[1]
	}
	return id
}
