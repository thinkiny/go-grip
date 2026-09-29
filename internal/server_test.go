package internal

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectoryListingIgnoresCacheValidators(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Hello\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	server := NewServer("localhost", 6419, false, false, false, NewParser())
	handler := server.newHandler(http.Dir(tmpDir))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected Cache-Control to disable storage, got %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "README.md") {
		t.Fatalf("expected directory listing body to mention README.md, got %q", recorder.Body.String())
	}
}

func TestRegularFileStillSupportsConditionalRequests(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "plain.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write plain.txt: %v", err)
	}

	server := NewServer("localhost", 6419, false, false, false, NewParser())
	handler := server.newHandler(http.Dir(tmpDir))

	req := httptest.NewRequest(http.MethodGet, "/plain.txt", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotModified {
		t.Fatalf("expected status %d, got %d", http.StatusNotModified, recorder.Code)
	}
}

func TestMarkdownResponsesDisableCaching(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Hello\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	server := NewServer("localhost", 6419, false, false, false, NewParser())
	handler := server.newHandler(http.Dir(tmpDir))

	req := httptest.NewRequest(http.MethodGet, "/README.md", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected Cache-Control to disable storage, got %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html" {
		t.Fatalf("expected text/html response, got %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "Hello") {
		t.Fatalf("expected rendered markdown response to contain document content, got %q", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "<title>README</title>") {
		t.Fatalf("expected filename-based HTML title, got %q", recorder.Body.String())
	}
}

func TestFormatFilenameTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{name: "humanizes separators", filename: "my-guide_v2.md", want: "My Guide V2"},
		{name: "uses basename", filename: "/docs/getting-started.md", want: "Getting Started"},
		{name: "preserves acronym", filename: "README.md", want: "README"},
		{name: "strips uppercase extension", filename: "release_notes.MD", want: "Release Notes"},
		{name: "collapses separators and whitespace", filename: "  release---notes__v2.md", want: "Release Notes V2"},
		{name: "supports unicode", filename: "überblick-plan.md", want: "Überblick Plan"},
		{name: "falls back for empty stem", filename: ".md", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatFilenameTitle(tt.filename); got != tt.want {
				t.Fatalf("formatFilenameTitle(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestFilenameTitleResponses(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	files := []string{"my-guide_v2.md", "unsafe-<script>.md", ".md"}
	for _, filename := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, filename), []byte("# Hello\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
	}

	server := NewServer("localhost", 6419, false, false, false, NewParser())
	handler := server.newHandler(http.Dir(tmpDir))

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "humanized title", path: "/my-guide_v2.md", want: "<title>My Guide V2</title>"},
		{name: "escaped title", path: "/unsafe-%3Cscript%3E.md", want: "<title>Unsafe &lt;script&gt;</title>"},
		{name: "empty title fallback", path: "/.md", want: "<title>" + defaultHTMLTitle + "</title>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), tt.want) {
				t.Fatalf("expected response to contain %q, got %q", tt.want, recorder.Body.String())
			}
		})
	}
}

func TestSourceFileRenderedWithHighlighting(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	server := NewServer("localhost", 6419, false, false, false, NewParser())
	handler := server.newHandler(http.Dir(tmpDir))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/main.go", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "chroma") {
		t.Fatalf("expected chroma markup, got %q", body)
	}
	if !strings.Contains(body, `<span class="kd">`) {
		t.Fatalf("expected keyword span for package, got %q", body)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("expected text/html, got %q", got)
	}
}

func TestServingPaths(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repository, "service"), 0o755); err != nil {
		t.Fatalf("mkdir service: %v", err)
	}

	tests := []struct {
		name         string
		file         string
		root         string
		wantDir      string
		wantRelative string
	}{
		{
			name:         "root anchors subdirectory file",
			file:         filepath.Join(repository, "service", "main.go"),
			root:         repository,
			wantDir:      repository,
			wantRelative: "service/main.go",
		},
		{
			name:         "file at root",
			file:         filepath.Join(repository, "README.md"),
			root:         repository,
			wantDir:      repository,
			wantRelative: "README.md",
		},
		{
			name:         "file outside root falls back to file directory",
			file:         filepath.Join(outside, "notes.md"),
			root:         repository,
			wantDir:      outside,
			wantRelative: "notes.md",
		},
		{
			name:         "empty root serves file directory",
			file:         filepath.Join(repository, "service", "main.go"),
			root:         "",
			wantDir:      filepath.Join(repository, "service"),
			wantRelative: "main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotDir, gotRelative := servingPaths(tt.file, tt.root)
			if gotDir != tt.wantDir {
				t.Fatalf("servingPaths(%q, %q) dir = %q, want %q", tt.file, tt.root, gotDir, tt.wantDir)
			}
			if gotRelative != tt.wantRelative {
				t.Fatalf("servingPaths(%q, %q) relative = %q, want %q", tt.file, tt.root, gotRelative, tt.wantRelative)
			}
		})
	}
}

func TestPlainTextFilesStayWithFileServer(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	server := NewServer("localhost", 6419, false, false, false, NewParser())
	handler := server.newHandler(http.Dir(tmpDir))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/notes.txt", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "chroma") {
		t.Fatalf("expected raw file body, got %q", recorder.Body.String())
	}
}
