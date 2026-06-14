package linuxpetdex

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"codex-pets/internal/protocol"
)

func TestAllowedBridgeAction(t *testing.T) {
	allowed := []string{
		"importPet",
		"listBrowserPets",
		"listInstalledPets",
		"selectInstalledPet",
		"installPiExtension",
		"uninstallPiExtension",
		"getPiExtensionStatus",
	}
	for _, action := range allowed {
		if !allowedBridgeAction(action) {
			t.Fatalf("%s should be allowed", action)
		}
	}
	for _, action := range []string{"", "openShell", "eval", "smokeReport"} {
		if allowedBridgeAction(action) {
			t.Fatalf("%s should not be allowed", action)
		}
	}
}

func TestBrowserRowPayload(t *testing.T) {
	row := protocol.BrowserPetRow{
		PetRef: protocol.PetRef{
			ID:              "app:boba:/tmp/boba",
			Slug:            "boba",
			DisplayName:     "Boba",
			Description:     "Bundled pet",
			Kind:            "creature",
			Source:          "app",
			SpritesheetPath: "sprite.webp",
			FrameWidth:      192,
			FrameHeight:     208,
		},
		Installed:    true,
		CanUninstall: true,
	}

	payload := browserRowPayload(row, func(id string, spriteName string) string {
		return "asset://" + id + "/" + spriteName
	})
	if payload["source"] != "installed" || payload["submittedBy"] != "Imported" {
		t.Fatalf("installed payload source labels = %+v", payload)
	}
	if payload["nativePetId"] != row.ID || payload["spritesheetUrl"] != "asset://app:boba:/tmp/boba/sprite.webp" {
		t.Fatalf("installed payload identity = %+v", payload)
	}
	if payload["frameWidth"] != 192 || payload["frameHeight"] != 208 || payload["canUninstall"] != true {
		t.Fatalf("installed payload dimensions/actions = %+v", payload)
	}

	row.Installed = false
	row.CanUninstall = false
	row.Attribution = "CodexPets"
	payload = browserRowPayload(row, func(id string, spriteName string) string {
		return "asset://" + id + "/" + spriteName
	})
	if payload["source"] != "prebundled" || payload["submittedBy"] != "CodexPets" {
		t.Fatalf("catalog payload source labels = %+v", payload)
	}
}

func TestPetListEventDetailIncludesSelectedPetID(t *testing.T) {
	pets := []map[string]any{{"nativePetId": "app:boba:/pets/boba"}}
	detail := petListEventDetail(pets, "app:boba:/pets/boba")

	if detail["selectedPetId"] != "app:boba:/pets/boba" {
		t.Fatalf("selectedPetId = %q", detail["selectedPetId"])
	}
	if got := detail["pets"].([]map[string]any); len(got) != 1 || got[0]["nativePetId"] != "app:boba:/pets/boba" {
		t.Fatalf("pets detail = %+v", detail["pets"])
	}
}

func TestAssetServerServesOnlyKnownAssets(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	petDir := filepath.Join(root, "prebundled-pets", "pets", "boba")
	if err := os.MkdirAll(petDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(petDir, "sprite.webp"), []byte("RIFFxxxxWEBPdata"), 0o644); err != nil {
		t.Fatal(err)
	}

	rows := []protocol.BrowserPetRow{{
		PetRef: protocol.PetRef{
			ID:              "prebundled:boba:" + petDir,
			Path:            petDir,
			SpritesheetPath: "sprite.webp",
		},
	}}
	server, err := newAssetServer(root, func() []protocol.BrowserPetRow {
		return append([]protocol.BrowserPetRow(nil), rows...)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()

	body := getBody(t, server.pageURL("index.html"), http.StatusOK)
	if body != "ok" {
		t.Fatalf("index body = %q", body)
	}
	spriteBody := getBody(t, server.assetURL(rows[0].ID, "sprite.webp"), http.StatusOK)
	if spriteBody != "RIFFxxxxWEBPdata" {
		t.Fatalf("sprite body = %q", spriteBody)
	}
	getBody(t, server.pageURL("../../go.mod"), http.StatusNotFound)
	getBody(t, server.assetURL("missing", "sprite.webp"), http.StatusNotFound)
}

func getBody(t *testing.T, url string, wantStatus int) string {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s status = %d, want %d; body %q", url, response.StatusCode, wantStatus, string(body))
	}
	return string(body)
}
