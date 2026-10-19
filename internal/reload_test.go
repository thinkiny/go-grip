package internal

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolveReloadPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docs := filepath.Join(root, "docs and notes")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	preview := filepath.Join(docs, "guide.md")
	if err := os.WriteFile(preview, []byte("# Guide\n"), 0o644); err != nil {
		t.Fatalf("write preview: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "nested escaped path", path: "/docs%20and%20notes/guide.md", want: preview},
		{name: "missing path remains addressable", path: "/missing.md", want: filepath.Join(root, "missing.md")},
		{name: "directory path", path: "/docs%20and%20notes/", wantErr: true},
		{name: "empty path", path: "/", wantErr: true},
		{name: "traversal", path: "/%2e%2e/outside.md", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveReloadPath(root, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveReloadPath(%q) = %q, want error", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveReloadPath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("resolveReloadPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestReloadHandlerIgnoresUnrelatedFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	preview := writeReloadFixture(t, root, "preview.md")
	unrelated := writeReloadFixture(t, root, "application.log")
	server := newReloadTestServer(t, root)
	baseline := reloadVersionForTest(t, preview)

	if err := os.WriteFile(unrelated, []byte("still running\n"), 0o644); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	if got := fetchReloadVersion(t, server.URL, "/preview.md"); got != baseline {
		t.Fatalf("version changed after unrelated write: got %q, want %q", got, baseline)
	}

	if err := os.WriteFile(preview, []byte("# Updated content\n"), 0o644); err != nil {
		t.Fatalf("write preview file: %v", err)
	}
	if got := fetchReloadVersion(t, server.URL, "/preview.md"); got == baseline {
		t.Fatalf("version remained %q after preview write", got)
	}
}

func TestReloadHandlerDetectsAtomicReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	preview := writeReloadFixture(t, root, "preview.md")
	server := newReloadTestServer(t, root)
	baseline := reloadVersionForTest(t, preview)

	replacement := filepath.Join(root, ".preview.md.new")
	if err := os.WriteFile(replacement, []byte("# Replaced content\n"), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Rename(replacement, preview); err != nil {
		t.Fatalf("replace preview: %v", err)
	}
	if got := fetchReloadVersion(t, server.URL, "/preview.md"); got == baseline {
		t.Fatalf("version remained %q after atomic replacement", got)
	}
}

func TestReloadVersionDetectsPreservedTimestampAndSize(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	preview := filepath.Join(root, "preview.md")
	if err := os.WriteFile(preview, []byte("first value\n"), 0o644); err != nil {
		t.Fatalf("write initial preview: %v", err)
	}
	info, err := os.Stat(preview)
	if err != nil {
		t.Fatalf("stat initial preview: %v", err)
	}
	baseline := reloadVersionForTest(t, preview)

	if err := os.WriteFile(preview, []byte("other value\n"), 0o644); err != nil {
		t.Fatalf("replace preview content: %v", err)
	}
	if err := os.Chtimes(preview, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore preview timestamp: %v", err)
	}
	if got := reloadVersionForTest(t, preview); got == baseline {
		t.Fatalf("content version remained %q with preserved timestamp and size", got)
	}
}

func TestReloadHandlerDetectsRemoval(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	preview := writeReloadFixture(t, root, "preview.md")
	server := newReloadTestServer(t, root)
	if err := os.Remove(preview); err != nil {
		t.Fatalf("remove preview: %v", err)
	}
	if got := fetchReloadVersion(t, server.URL, "/preview.md"); got != reloadMissingVersion {
		t.Fatalf("removed preview version = %q, want %q", got, reloadMissingVersion)
	}
}

func TestReloadHandlerSeparatesFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := writeReloadFixture(t, root, "first.md")
	second := writeReloadFixture(t, root, "second.md")
	server := newReloadTestServer(t, root)
	firstBaseline := reloadVersionForTest(t, first)
	secondBaseline := reloadVersionForTest(t, second)

	if err := os.WriteFile(first, []byte("# First updated content\n"), 0o644); err != nil {
		t.Fatalf("write first preview: %v", err)
	}
	if got := fetchReloadVersion(t, server.URL, "/first.md"); got == firstBaseline {
		t.Fatalf("first version remained %q after write", got)
	}
	if got := fetchReloadVersion(t, server.URL, "/second.md"); got != secondBaseline {
		t.Fatalf("second version changed to %q, want %q", got, secondBaseline)
	}
}

func TestReloadHandlerCompletesConcurrentPolls(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	preview := writeReloadFixture(t, root, "preview.md")
	server := newReloadTestServer(t, root)
	want := reloadVersionForTest(t, preview)
	const polls = 20

	errors := make(chan error, polls)
	var wait sync.WaitGroup
	for range polls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if got := fetchReloadVersionWithoutTest(server.URL, "/preview.md"); got != want {
				errors <- &versionMismatchError{got: got, want: want}
			}
		}()
	}
	finished := make(chan struct{})
	go func() {
		wait.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent reload polls did not complete")
	}
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestReloadScriptMatchesHandlerAvailability(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	preview := writeReloadFixture(t, root, "preview.md")
	reloader, err := newReloadHandler(root)
	if err != nil {
		t.Fatalf("create reload handler: %v", err)
	}

	for _, tt := range []struct {
		name       string
		reloader   http.Handler
		wantScript bool
	}{
		{name: "reload enabled", reloader: reloader, wantScript: true},
		{name: "reload disabled", reloader: nil, wantScript: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer("localhost", 6419, false, false, tt.wantScript, NewParser())
			recorder := httptest.NewRecorder()
			server.newHandler(http.Dir(root), tt.reloader).ServeHTTP(
				recorder,
				httptest.NewRequest(http.MethodGet, "/preview.md", nil),
			)

			body := recorder.Body.String()
			gotScript := strings.Contains(body, `src="/static/js/reload.js"`)
			if gotScript != tt.wantScript {
				t.Fatalf("reload script presence = %t, want %t", gotScript, tt.wantScript)
			}
			if tt.wantScript {
				version := reloadVersionForTest(t, preview)
				if !strings.Contains(body, `data-version="`+version+`"`) {
					t.Fatalf("reload script did not contain content version %q", version)
				}
			}
		})
	}
}

func TestSourcePreviewIncludesReloadScript(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	sourceBytes := []byte("package main\n")
	if err := os.WriteFile(source, sourceBytes, 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	reloader, err := newReloadHandler(root)
	if err != nil {
		t.Fatalf("create reload handler: %v", err)
	}
	server := NewServer("localhost", 6419, false, false, true, NewParser())
	recorder := httptest.NewRecorder()
	server.newHandler(http.Dir(root), reloader).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/main.go", nil),
	)

	body := recorder.Body.String()
	if !strings.Contains(body, `src="/static/js/reload.js"`) {
		t.Fatal("source preview did not include reload script")
	}
	if !strings.Contains(body, `data-version="`+contentVersion(sourceBytes)+`"`) {
		t.Fatal("source preview did not include content version")
	}
}

func TestReloadScriptAsset(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server := NewServer("localhost", 6419, false, false, false, NewParser())
	recorder := httptest.NewRecorder()
	server.newHandler(http.Dir(root), nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/static/js/reload.js", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("reload script status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "fetch(") {
		t.Fatal("reload script asset did not contain polling client")
	}
	if strings.Contains(body, "EventSource") || strings.Contains(body, "WebSocket") {
		t.Fatal("reload script asset still contained a persistent connection client")
	}
}

func writeReloadFixture(t *testing.T, root string, name string) string {
	t.Helper()

	filename := filepath.Join(root, name)
	if err := os.WriteFile(filename, []byte("# Preview\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return filename
}

func reloadVersionForTest(t *testing.T, filename string) string {
	t.Helper()

	version, err := reloadFileVersion(filename)
	if err != nil {
		t.Fatalf("read reload version for %s: %v", filename, err)
	}
	return version
}

func newReloadTestServer(t *testing.T, root string) *httptest.Server {
	t.Helper()

	handler, err := newReloadHandler(root)
	if err != nil {
		t.Fatalf("create reload handler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func fetchReloadVersion(t *testing.T, serverURL string, previewPath string) string {
	t.Helper()

	version := fetchReloadVersionWithoutTest(serverURL, previewPath)
	if strings.HasPrefix(version, "error: ") {
		t.Fatal(version)
	}
	return version
}

func fetchReloadVersionWithoutTest(serverURL string, previewPath string) string {
	query := url.Values{"path": []string{previewPath}}
	response, err := http.Get(serverURL + reloadEndpoint + "?" + query.Encode())
	if err != nil {
		return "error: poll reload version: " + err.Error()
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "error: read reload version: " + err.Error()
	}
	if response.StatusCode != http.StatusOK {
		return "error: reload version status " + response.Status + ": " + string(body)
	}
	return string(body)
}

type versionMismatchError struct {
	got  string
	want string
}

func (err *versionMismatchError) Error() string {
	return "reload version = " + err.got + ", want " + err.want
}
