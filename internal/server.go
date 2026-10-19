package internal

import (
	"bytes"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	chroma_html "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/chrishrb/go-grip/defaults"
)

const defaultHTMLTitle = "go-grip - markdown preview"

type Server struct {
	parser       *Parser
	boundingBox  bool
	host         string
	port         int
	browser      bool
	enableReload bool
}

func NewServer(host string, port int, boundingBox bool, browser bool, enableReload bool, parser *Parser) *Server {
	return &Server{
		host:         host,
		port:         port,
		boundingBox:  boundingBox,
		browser:      browser,
		enableReload: enableReload,
		parser:       parser,
	}
}

func (s *Server) Serve(file string, root string) error {
	servingDirectory, relativePath := servingPaths(file, root)
	dir := http.Dir(servingDirectory)

	var reloader http.Handler
	if s.enableReload {
		reloadHandler, err := newReloadHandler(servingDirectory)
		if err != nil {
			return err
		}
		reloader = reloadHandler
	}
	handler := s.newHandler(dir, reloader)

	addr := fmt.Sprintf("http://%s:%d/", s.host, s.port)
	if file == "" {
		// If README.md exists then open README.md at beginning
		readme := "README.md"
		f, err := dir.Open(readme)
		if err == nil {
			//nolint:errcheck
			defer f.Close()
		}
		if err == nil {
			addr, _ = url.JoinPath(addr, readme)
		}
	} else {
		addr, _ = url.JoinPath(addr, relativePath)
	}

	fmt.Printf("🚀 Starting server: %s\n", addr)

	if s.browser {
		err := Open(addr)
		if err != nil {
			fmt.Println("❌ Error opening browser:", err)
		}
	}

	if s.enableReload {
		fmt.Printf("📡 Auto-reload enabled. Changes to the current page will trigger browser refresh.\n")
	} else {
		fmt.Printf("🔄 Auto-reload disabled. Use F5 to manually refresh.\n")
	}
	return http.ListenAndServe(fmt.Sprintf("%s:%d", s.host, s.port), handler)
}

// servingPaths returns the directory to serve and the initial page path. The
// URL namespace is anchored at root when given (the Emacs preview passes the
// projectile project root) so relative links across subdirectories resolve
// instead of clamping at "/" and 404ing; without root it is the file's own
// directory.
func servingPaths(file string, root string) (string, string) {
	directory := filepath.Dir(file)
	filename := filepath.Base(file)
	if file == "" {
		return directory, filename
	}

	absolutePath, err := filepath.Abs(file)
	if err != nil {
		return directory, filename
	}

	anchor := directory
	if root != "" {
		if absoluteRoot, rootErr := filepath.Abs(root); rootErr == nil {
			anchor = absoluteRoot
		}
	}

	relative, relErr := filepath.Rel(anchor, absolutePath)
	outsideAnchor := relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if relErr != nil || outsideAnchor {
		return directory, filename
	}
	return anchor, filepath.ToSlash(relative)
}

func (s *Server) newHandler(dir http.Dir, reloader http.Handler) http.Handler {
	fileServer := http.FileServer(dir)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(defaults.StaticFiles)))
	if reloader != nil {
		mux.Handle(reloadEndpoint, reloader)
	}

	regex := regexp.MustCompile(`(?i)\.md$`)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if regex.MatchString(r.URL.Path) {
			isFile, err := isRegularFile(dir, r.URL.Path)
			if err == nil && isFile {
				setNoCacheHeaders(w)

				bytes, err := readToString(dir, r.URL.Path)
				if err != nil {
					log.Fatal(err)
					return
				}
				htmlContent, err := s.parser.MdToHTML(bytes)
				if err != nil {
					log.Fatal(err)
					return
				}

				err = serveTemplate(w, htmlStruct{
					Content:       string(htmlContent),
					BoundingBox:   s.boundingBox,
					CssCodeLight:  getCssCode("github"),
					CssCodeDark:   getCssCode("github-dark"),
					Title:         html.EscapeString(s.pageTitle(r.URL.Path)),
					ReloadVersion: pageReloadVersion(bytes, reloader != nil),
				})
				if err != nil {
					log.Fatal(err)
					return
				}
				return
			}
		}

		if lexers.Match(path.Base(r.URL.Path)) != nil {
			isFile, err := isRegularFile(dir, r.URL.Path)
			if err == nil && isFile && s.renderSourceFile(w, dir, r.URL.Path, reloader != nil) {
				return
			}
		}

		isDirectory, err := isDirectory(dir, r.URL.Path)
		if err == nil && isDirectory {
			setNoCacheHeaders(w)
			stripCacheValidators(r)
		}

		fileServer.ServeHTTP(w, r)
	})

	return mux
}

// renderSourceFile writes requestPath as a syntax-highlighted HTML page when
// the file name maps to a chroma lexer, and reports whether it did. Source
// files would otherwise be served as plain text by the file server.
func (s *Server) renderSourceFile(w http.ResponseWriter, dir http.Dir, requestPath string, reloadEnabled bool) bool {
	filename := path.Base(requestPath)
	lexer := lexers.Match(filename)
	if lexer == nil || lexer.Config().Name == "plaintext" {
		return false
	}

	sourceBytes, err := readToString(dir, requestPath)
	if err != nil {
		return false
	}

	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, string(sourceBytes))
	if err != nil {
		return false
	}

	formatter := chroma_html.New(
		chroma_html.WithClasses(true),
		chroma_html.WithLineNumbers(true),
		chroma_html.LineNumbersInTable(true),
	)
	var highlighted bytes.Buffer
	if err := formatter.Format(&highlighted, styles.Get("github"), iterator); err != nil {
		return false
	}

	setNoCacheHeaders(w)
	err = serveTemplate(w, htmlStruct{
		Content:       highlighted.String(),
		BoundingBox:   s.boundingBox,
		CssCodeLight:  getCssCode("github"),
		CssCodeDark:   getCssCode("github-dark"),
		Title:         html.EscapeString(filename),
		ReloadVersion: pageReloadVersion(sourceBytes, reloadEnabled),
	})
	return err == nil
}

func pageReloadVersion(contents []byte, enabled bool) string {
	if !enabled {
		return ""
	}
	return contentVersion(contents)
}

func readToString(dir http.Dir, filename string) ([]byte, error) {
	f, err := dir.Open(filename)
	if err != nil {
		return nil, err
	}
	//nolint:errcheck
	defer f.Close()

	var buf bytes.Buffer
	_, err = buf.ReadFrom(f)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type htmlStruct struct {
	Content       string
	BoundingBox   bool
	CssCodeLight  string
	CssCodeDark   string
	Title         string
	ReloadVersion string
}

func (s *Server) pageTitle(filename string) string {
	title := formatFilenameTitle(filename)
	if title == "" {
		return defaultHTMLTitle
	}
	return title
}

func formatFilenameTitle(filename string) string {
	filename = path.Base(filename)
	extension := path.Ext(filename)
	if strings.EqualFold(extension, ".md") {
		filename = strings.TrimSuffix(filename, extension)
	}

	filename = strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, filename)

	words := strings.Fields(filename)
	for i, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[i] = string(unicode.ToUpper(first)) + word[size:]
	}
	return strings.Join(words, " ")
}

func serveTemplate(w http.ResponseWriter, html htmlStruct) error {
	w.Header().Set("Content-Type", "text/html")
	tmpl, err := template.ParseFS(defaults.Templates, "templates/layout.html")
	if err != nil {
		return err
	}
	err = tmpl.Execute(w, html)
	return err
}

func getCssCode(style string) string {
	buf := new(strings.Builder)
	formatter := chroma_html.New(chroma_html.WithClasses(true))
	s := styles.Get(style)
	_ = formatter.WriteCSS(buf, s)
	return buf.String()
}

func setNoCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func stripCacheValidators(r *http.Request) {
	r.Header.Del("If-Modified-Since")
	r.Header.Del("If-None-Match")
}

func isDirectory(dir http.Dir, name string) (bool, error) {
	file, err := dir.Open(name)
	if err != nil {
		return false, err
	}
	//nolint:errcheck
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return false, err
	}

	return info.IsDir(), nil
}

func isRegularFile(dir http.Dir, name string) (bool, error) {
	file, err := dir.Open(name)
	if err != nil {
		return false, err
	}
	//nolint:errcheck
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return false, err
	}

	return !info.IsDir(), nil
}
