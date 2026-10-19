package internal

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	reloadEndpoint       = "/reload-version"
	reloadMissingVersion = "missing"
)

type reloadHandler struct {
	root string
}

func newReloadHandler(root string) (*reloadHandler, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve reload root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect reload root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("reload root %q is not a directory", absoluteRoot)
	}
	return &reloadHandler{root: absoluteRoot}, nil
}

func (handler *reloadHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	filename, err := resolveReloadPath(handler.root, request.URL.Query().Get("path"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	version, err := reloadFileVersion(filename)
	if err != nil {
		if !os.IsNotExist(err) {
			http.Error(writer, "read preview version", http.StatusInternalServerError)
			return
		}
		version = reloadMissingVersion
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write([]byte(version))
}

func resolveReloadPath(root string, requestPath string) (string, error) {
	decodedPath, err := url.PathUnescape(requestPath)
	if err != nil {
		return "", fmt.Errorf("decode preview path: %w", err)
	}
	relativePath := filepath.FromSlash(strings.TrimLeft(decodedPath, "/"))
	if relativePath == "" || relativePath == "." {
		return "", errors.New("preview path must identify a file")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve reload root: %w", err)
	}
	filename := filepath.Clean(filepath.Join(absoluteRoot, relativePath))
	relativeToRoot, err := filepath.Rel(absoluteRoot, filename)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", errors.New("preview path is outside the served directory")
	}
	if info, statErr := os.Stat(filename); statErr == nil && !info.Mode().IsRegular() {
		return "", errors.New("preview path must identify a regular file")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect preview path: %w", statErr)
	}
	return filename, nil
}

func reloadFileVersion(filename string) (string, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return contentVersion(contents), nil
}

func contentVersion(contents []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(contents))
}
