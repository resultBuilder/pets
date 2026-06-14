package linuxpetdex

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"codex-pets/internal/protocol"
)

type assetServer struct {
	root string
	rows func() []protocol.BrowserPetRow

	token    string
	listener net.Listener
	server   *http.Server
}

func newAssetServer(root string, rows func() []protocol.BrowserPetRow) (*assetServer, error) {
	if strings.TrimSpace(root) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		root = cwd
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &assetServer{
		root:     root,
		rows:     rows,
		token:    randomToken(),
		listener: listener,
	}
	server.server = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		_ = server.server.Serve(listener)
	}()
	return server, nil
}

func (s *assetServer) close() {
	if s == nil || s.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}

func (s *assetServer) pageURL(file string) string {
	return s.baseURL() + strings.TrimLeft(file, "/")
}

func (s *assetServer) assetURL(petID string, spriteName string) string {
	ext := strings.ToLower(filepath.Ext(spriteName))
	if ext == "" {
		ext = ".webp"
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(petID))
	return s.baseURL() + "_codexpets_asset/pet/" + encoded + "/spritesheet" + ext
}

func (s *assetServer) baseURL() string {
	return "http://" + s.listener.Addr().String() + "/" + s.token + "/"
}

func (s *assetServer) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	prefix := "/" + s.token + "/"
	if request.Method != http.MethodGet || !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(response, request)
		return
	}
	relative := strings.TrimPrefix(request.URL.Path, prefix)
	if strings.HasPrefix(relative, "_codexpets_asset/pet/") {
		s.servePetAsset(response, request, relative)
		return
	}
	s.serveStatic(response, request, relative)
}

func (s *assetServer) serveStatic(response http.ResponseWriter, request *http.Request, relative string) {
	clean := path.Clean("/" + relative)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." || clean == "" {
		clean = "index.html"
	}
	if !allowedStaticPath(clean) {
		http.NotFound(response, request)
		return
	}
	filePath := filepath.Join(s.root, filepath.FromSlash(clean))
	if !isWithin(s.root, filePath) {
		http.NotFound(response, request)
		return
	}
	setContentType(response, filePath)
	http.ServeFile(response, request, filePath)
}

func (s *assetServer) servePetAsset(response http.ResponseWriter, request *http.Request, relative string) {
	parts := strings.Split(relative, "/")
	if len(parts) != 4 || parts[0] != "_codexpets_asset" || parts[1] != "pet" {
		http.NotFound(response, request)
		return
	}
	rawID, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		http.NotFound(response, request)
		return
	}
	petID := string(rawID)
	var row protocol.BrowserPetRow
	found := false
	for _, candidate := range s.rows() {
		if candidate.ID == petID {
			row = candidate
			found = true
			break
		}
	}
	if !found || row.Path == "" || row.SpritesheetPath == "" {
		http.NotFound(response, request)
		return
	}
	filePath := filepath.Join(row.Path, row.SpritesheetPath)
	if !isWithin(row.Path, filePath) {
		http.NotFound(response, request)
		return
	}
	setContentType(response, filePath)
	http.ServeFile(response, request, filePath)
}

func allowedStaticPath(relative string) bool {
	switch relative {
	case "index.html", "styles.css", "app.js":
		return true
	default:
		return strings.HasPrefix(relative, "prebundled-pets/")
	}
}

func isWithin(root string, filePath string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel))
}

func setContentType(response http.ResponseWriter, filePath string) {
	if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))); contentType != "" {
		response.Header().Set("Content-Type", contentType)
		return
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".webp":
		response.Header().Set("Content-Type", "image/webp")
	case ".js":
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".css":
		response.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".html":
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
	default:
		response.Header().Set("Content-Type", "application/octet-stream")
	}
}

func randomToken() string {
	var data [18]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}
