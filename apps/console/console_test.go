package console

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"amdl/internal/client"
)

// wantCSP is written out independently of ContentSecurityPolicy so a change
// to the policy has to be made in two places on purpose.
const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; " +
	"media-src 'self' https: blob:; connect-src 'self' https://mvod.itunes.apple.com; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// vendorFile and vendorSHA256 pin the vendored Preact+htm bundle; see AGENTS.md.
const (
	vendorFile   = "vendor/htm-preact-standalone-3.1.1.mjs"
	vendorSHA256 = "72284e8e9079c87817145df1110f74e8a2aa040b2fc384922e18dfcb46fc1fd7"
)

// mounted serves the console the way the API router mounts it.
func mounted() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /console/", http.StripPrefix("/console", Handler()))
	return mux
}

func get(t *testing.T, h http.Handler, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Result().Header
	if got := h.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := h.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func staticFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		t.Fatalf("open embedded static: %v", err)
	}
	return sub
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(staticFS(t), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestIndexServedWithSecurityHeaders(t *testing.T) {
	rec := get(t, mounted(), http.MethodGet, "/console/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /console/ = %d, want 200", rec.Code)
	}
	assertSecurityHeaders(t, rec)
	h := rec.Result().Header
	if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := h.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if etag := h.Get("ETag"); !regexp.MustCompile(`^"[0-9a-f]{32}"$`).MatchString(etag) {
		t.Errorf("ETag = %q, want a quoted strong hash", etag)
	}
	if !regexp.MustCompile(`<html lang="en"[ >]`).MatchString(rec.Body.String()) {
		t.Errorf("index.html does not declare lang=\"en\"")
	}
	// The default skin is static so the first frame, before main.js runs, has every skin token.
	if !strings.Contains(rec.Body.String(), `data-skin="music"`) {
		t.Errorf("index.html does not set the default data-skin=\"music\" on <html>")
	}
}

func TestIndexAtExplicitPath(t *testing.T) {
	a := get(t, mounted(), http.MethodGet, "/console/", nil)
	b := get(t, mounted(), http.MethodGet, "/console/index.html", nil)
	if b.Code != http.StatusOK || a.Body.String() != b.Body.String() {
		t.Fatalf("/console/index.html = %d, want the same page as /console/", b.Code)
	}
}

func TestConditionalGetReturns304(t *testing.T) {
	first := get(t, mounted(), http.MethodGet, "/console/css/base.css", nil)
	etag := first.Result().Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on first response")
	}
	rec := get(t, mounted(), http.MethodGet, "/console/css/base.css", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match with the current ETag = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried a %d-byte body", rec.Body.Len())
	}
	assertSecurityHeaders(t, rec)

	stale := get(t, mounted(), http.MethodGet, "/console/css/base.css", map[string]string{"If-None-Match": `"0000"`})
	if stale.Code != http.StatusOK {
		t.Errorf("If-None-Match with another ETag = %d, want 200", stale.Code)
	}
}

func TestETagFollowsContent(t *testing.T) {
	a := get(t, mounted(), http.MethodGet, "/console/css/base.css", nil).Result().Header.Get("ETag")
	b := get(t, mounted(), http.MethodGet, "/console/css/tokens.css", nil).Result().Header.Get("ETag")
	if a == b {
		t.Errorf("two different files share ETag %s", a)
	}
}

func TestCacheControl(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/console/" + vendorFile, "public, max-age=31536000, immutable"},
		{"/console/js/main.js", "no-cache"},
		{"/console/css/tokens.css", "no-cache"},
		{"/console/icons.svg", "no-cache"},
	}
	for _, c := range cases {
		rec := get(t, mounted(), http.MethodGet, c.path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", c.path, rec.Code)
			continue
		}
		if got := rec.Result().Header.Get("Cache-Control"); got != c.want {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestContentTypes(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/console/js/main.js", "text/javascript; charset=utf-8"},
		{"/console/" + vendorFile, "text/javascript; charset=utf-8"},
		{"/console/icons.svg", "image/svg+xml"},
		{"/console/css/base.css", "text/css; charset=utf-8"},
	}
	for _, c := range cases {
		rec := get(t, mounted(), http.MethodGet, c.path, nil)
		if got := rec.Result().Header.Get("Content-Type"); got != c.want {
			t.Errorf("GET %s Content-Type = %q, want %q", c.path, got, c.want)
		}
		assertSecurityHeaders(t, rec)
	}
}

func TestMissingFilesAndDirectories(t *testing.T) {
	for _, p := range []string{"/nope.js", "/css/", "/css", "/js", "/../console.go", "/static/index.html", "/js/../../console.go"} {
		rec := get(t, Handler(), http.MethodGet, p, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
		assertSecurityHeaders(t, rec)
	}
}

func TestHeadAndMethods(t *testing.T) {
	head := get(t, Handler(), http.MethodHead, "/", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD / = %d with %d body bytes, want 200 and none", head.Code, head.Body.Len())
	}
	post := get(t, Handler(), http.MethodPost, "/", nil)
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", post.Code)
	}
	assertSecurityHeaders(t, post)
}

func TestVendorChecksum(t *testing.T) {
	sum := sha256.Sum256([]byte(readStatic(t, vendorFile)))
	if got := hex.EncodeToString(sum[:]); got != vendorSHA256 {
		t.Fatalf("%s sha256 = %s, want %s; the vendored file must stay byte-identical to upstream (see AGENTS.md)", vendorFile, got, vendorSHA256)
	}
}

func TestEmbeddedFilesHaveKnownTypes(t *testing.T) {
	err := fs.WalkDir(staticFS(t), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, ok := contentTypes[path.Ext(name)]; !ok {
			t.Errorf("embedded %s has no known content type; stray files (.DS_Store, editor backups) ship in the binary", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var (
	attrURL    = regexp.MustCompile(`\b(?:src|href)="([^"]*)"`)
	importSpec = regexp.MustCompile(`(?m)(?:^\s*import\s+(?:[^'"]*?\s+from\s+)?|^\s*export\s+[^'"]*?\s+from\s+|\bimport\s*\(\s*)['"]([^'"]+)['"]`)
)

// TestIndexReferencesResolve checks that every relative src/href in
// index.html names an embedded file, so the page works under /console/.
func TestIndexReferencesResolve(t *testing.T) {
	files := staticFS(t)
	matches := attrURL.FindAllStringSubmatch(readStatic(t, "index.html"), -1)
	checked := 0
	for _, m := range matches {
		ref := m[1]
		if ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "../") || strings.Contains(ref, "://") || strings.HasPrefix(ref, "#") {
			continue
		}
		if strings.HasPrefix(ref, "/") {
			t.Errorf("index.html references %q with an absolute path; use a relative one so the console works under any prefix", ref)
			continue
		}
		if _, err := fs.Stat(files, ref); err != nil {
			t.Errorf("index.html references %q, which is not embedded", ref)
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("checked only %d references in index.html; the attribute pattern is broken", checked)
	}
}

func jsFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(staticFS(t), "js", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := path.Ext(name); ext == ".js" || ext == ".mjs" {
			out[name] = readStatic(t, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 5 {
		t.Fatalf("found only %d JS files under static/js", len(out))
	}
	return out
}

// TestModuleImportsResolve checks that every import in static/js is a
// relative specifier naming an embedded file: there is no bundler and no
// import map, so a bare or broken specifier fails in the browser.
func TestModuleImportsResolve(t *testing.T) {
	files := staticFS(t)
	total := 0
	for name, src := range jsFiles(t) {
		for _, m := range importSpec.FindAllStringSubmatch(src, -1) {
			spec := m[1]
			total++
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				t.Errorf("%s imports %q; only relative specifiers resolve without a bundler", name, spec)
				continue
			}
			target := path.Clean(path.Join(path.Dir(name), spec))
			if _, err := fs.Stat(files, target); err != nil {
				t.Errorf("%s imports %q, which resolves to %s and is not embedded", name, spec, target)
			}
		}
	}
	if total < 20 {
		t.Fatalf("found only %d imports; the import pattern is broken", total)
	}
}

var (
	colorLiteral    = regexp.MustCompile(`(?i)#[0-9a-f]{3}(?:[0-9a-f]{1,5})?\b|\b(?:rgba?|hsla?|hwb|oklch|oklab|lab|lch)\(`)
	transitionAll   = regexp.MustCompile(`(?i)transition(?:-property)?\s*:\s*all\b`)
	fontSizeDecl    = regexp.MustCompile(`font-size:\s*([^;]+);`)
	innerHTMLWrites = regexp.MustCompile(`\.(?:inner|outer)HTML\s*=|insertAdjacentHTML|dangerouslySetInnerHTML|document\.write`)
)

func hasCJK(s string) (rune, bool) {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return r, true
		}
	}
	return 0, false
}

// colorFile reports whether a static file may contain color literals: the
// skin-neutral tokens and the skin files, which own every color.
func colorFile(name string) bool {
	return name == "css/tokens.css" || (strings.HasPrefix(name, "css/skins/") && path.Ext(name) == ".css")
}

// designViolations returns what the design guard rejects in one file:
// color literals outside css/tokens.css and css/skins/*.css, "transition:
// all", CJK characters in UI copy, raw HTML writes, and font sizes that
// bypass the type scale.
func designViolations(name, text string) []string {
	var out []string
	if !colorFile(name) {
		for _, m := range colorLiteral.FindAllString(text, -1) {
			out = append(out, "color literal "+m+" (colors live in css/tokens.css and css/skins/*.css)")
		}
	}
	if m := transitionAll.FindString(text); m != "" {
		out = append(out, `"`+m+`" (name the properties that transition)`)
	}
	if r, ok := hasCJK(text); ok {
		out = append(out, "CJK character "+string(r)+" (UI copy is English; Apple data arrives at runtime)")
	}
	if path.Ext(name) == ".js" {
		if m := innerHTMLWrites.FindString(text); m != "" {
			out = append(out, m+" (render through htm, which escapes data)")
		}
	}
	if path.Ext(name) == ".css" {
		for _, m := range fontSizeDecl.FindAllStringSubmatch(text, -1) {
			if !strings.HasPrefix(strings.TrimSpace(m[1]), "var(--fs-") {
				out = append(out, "font-size: "+m[1]+" (use a --fs-* token)")
			}
		}
	}
	return out
}

func TestDesignGuard(t *testing.T) {
	scanned := 0
	err := fs.WalkDir(staticFS(t), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(name, vendorDir) {
			return err
		}
		scanned++
		for _, v := range designViolations(name, readStatic(t, name)) {
			t.Errorf("static/%s: %s", name, v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 10 {
		t.Fatalf("design guard scanned only %d files", scanned)
	}
}

func TestDesignGuardCatchesViolations(t *testing.T) {
	cases := []struct {
		name, file, text string
	}{
		{"hex color", "css/base.css", "a { color: #e35e38; }"},
		{"short hex", "js/x.js", "const c = '#fff';"},
		{"rgb", "css/base.css", "a { color: rgb(0 0 0); }"},
		{"oklch", "css/base.css", "a { color: oklch(0.5 0.1 20); }"},
		{"transition all", "css/base.css", "a { transition: all 200ms; }"},
		{"cjk", "js/x.js", "const label = '下载';"},
		{"innerHTML", "js/x.js", "el.innerHTML = name;"},
		{"raw font size", "css/base.css", "a { font-size: 13px; }"},
		{"raw font size in a skin", "css/skins/music.css", "a { font-size: 13px; }"},
		{"color in a js file under a skins-like path", "js/skins/x.js", "const c = '#fff';"},
		{"color in a non-css skin file", "css/skins/notes.txt", "#ffffff"},
	}
	for _, c := range cases {
		if len(designViolations(c.file, c.text)) == 0 {
			t.Errorf("%s: guard accepted %q", c.name, c.text)
		}
	}
	clean := []struct{ file, text string }{
		{"css/tokens.css", "--x: light-dark(#ffffff, #111113);"},
		{"css/skins/music.css", ":root[data-skin=\"music\"] { --x: light-dark(#ffffff, rgba(0, 0, 0, 0.5)); }"},
		{"css/skins/music.css", "a { font-size: var(--fs-page-title); }"},
		{"js/x.js", "location.hash = '#/queue'; const bg = `#${hex}`;"},
		{"css/shell.css", "a { font-size: var(--fs-12); transition: opacity 120ms; }"},
	}
	for _, c := range clean {
		if v := designViolations(c.file, c.text); len(v) != 0 {
			t.Errorf("guard rejected clean %s %q: %v", c.file, c.text, v)
		}
	}
}

func TestIndexHasNoInlineCode(t *testing.T) {
	index := readStatic(t, "index.html")
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]`),
		regexp.MustCompile(`<style`),
		regexp.MustCompile(`\sstyle="`),
		regexp.MustCompile(`\son[a-z]+="`),
	} {
		if m := re.FindString(index); m != "" {
			t.Errorf("index.html contains %q; the CSP blocks inline scripts, styles and handlers", m)
		}
	}
}

var (
	specPath    = regexp.MustCompile(`(?m)^  (/api/[^\s:]+):\s*$`)
	clientPath  = regexp.MustCompile(`/api/v1/(?:[A-Za-z0-9_\-./]|\$\{[^}]*\})*`)
	templateArg = regexp.MustCompile(`\$\{[^}]*\}`)
	specParam   = regexp.MustCompile(`\{[^}]+\}`)
)

// TestAPIPathsExistInSpec is the contract guard: every /api/v1 path the
// console calls must be a path in internal/api/openapi.yaml, so a renamed
// or removed route fails here before it fails in a browser.
func TestAPIPathsExistInSpec(t *testing.T) {
	spec, err := os.ReadFile("../../internal/api/openapi.yaml")
	if err != nil {
		t.Fatalf("read the API contract: %v", err)
	}
	known := map[string]bool{}
	for _, m := range specPath.FindAllStringSubmatch(string(spec), -1) {
		known[specParam.ReplaceAllString(m[1], "{param}")] = true
	}
	if len(known) < 10 {
		t.Fatalf("parsed only %d paths from openapi.yaml; the path pattern is broken", len(known))
	}
	used := map[string][]string{}
	for name, src := range jsFiles(t) {
		for _, m := range clientPath.FindAllString(src, -1) {
			p := strings.TrimRight(templateArg.ReplaceAllString(m, "{param}"), "/.")
			used[p] = append(used[p], name)
		}
	}
	if len(used) < 12 {
		t.Fatalf("found only %d distinct /api/v1 paths in static/js; the guard would pass vacuously", len(used))
	}
	paths := make([]string, 0, len(used))
	for p := range used {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !known[p] {
			files := used[p]
			sort.Strings(files)
			t.Errorf("static/js calls %s (in %s), which is not a path in internal/api/openapi.yaml", p, strings.Join(files, ", "))
		}
	}
}

var (
	componentTag  = regexp.MustCompile(`"component",\s*"([a-z_]+)"`)
	logComponents = regexp.MustCompile(`export const LOG_COMPONENTS = \[([^\]]*)\]`)
	quotedWord    = regexp.MustCompile(`'([a-z_]+)'`)
)

// The Logs page offers a component filter; a tag the backend logs with but the
// list lacks can only be reached by editing the URL.
func TestLogComponentsMatchBackend(t *testing.T) {
	backend := map[string]bool{}
	for _, root := range []string{"../amdl", "../../internal"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range componentTag.FindAllStringSubmatch(string(src), -1) {
				backend[m[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(backend) < 5 {
		t.Fatalf("found only %d component tags in the Go code; the pattern is broken", len(backend))
	}
	src, err := fs.ReadFile(embedded, "static/js/lib/route.js")
	if err != nil {
		t.Fatal(err)
	}
	list := logComponents.FindStringSubmatch(string(src))
	if list == nil {
		t.Fatal("LOG_COMPONENTS not found in lib/route.js")
	}
	console := map[string]bool{}
	for _, m := range quotedWord.FindAllStringSubmatch(list[1], -1) {
		console[m[1]] = true
	}
	for _, name := range slices.Sorted(maps.Keys(backend)) {
		if !console[name] {
			t.Errorf("the backend logs component %q, but LOG_COMPONENTS in lib/route.js lacks it", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(console)) {
		if !backend[name] {
			t.Errorf("LOG_COMPONENTS lists %q, which no Go code logs as a component", name)
		}
	}
}

var (
	varRef  = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)`)
	varDecl = regexp.MustCompile(`(?m)(?:^|[;{\s])(--[A-Za-z0-9_-]+)\s*:`)
	// Custom properties set per element from JS style objects or by the
	// role mapping, a job row without progress, the Queue's trailing slot
	// and a status head's title row in the base CSS itself, not by a skin.
	runtimeVars = map[string]bool{
		"--art-bg": true, "--art-t1": true, "--art-t2": true, "--art-t3": true,
		"--w": true, "--f": true, "--v": true, "--n": true, "--swatch": true,
		"--role": true, "--role-tint": true, "--new-bar-h": true, "--job-prog": true, "--job-trail": true, "--cols": true,
		"--save-x": true, "--save-w": true, "--domain-title-h": true,
		"--hook-more-w": true, "--hook-controls-w": true,
	}
)

func cssNames(re *regexp.Regexp, text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	return out
}

// baseCSS returns the base stylesheets, every css/*.css except tokens.css:
// the structure under the skin, split by area.
func baseCSS(t *testing.T) []string {
	t.Helper()
	names, err := fs.Glob(staticFS(t), "css/*.css")
	if err != nil {
		t.Fatal(err)
	}
	out := slices.DeleteFunc(names, func(n string) bool { return n == "css/tokens.css" })
	if len(out) < 5 {
		t.Fatalf("found only %d base stylesheets under css/", len(out))
	}
	return out
}

// TestSkinsDeclareEveryBaseToken keeps the skin complete: every custom
// property a base stylesheet (css/*.css other than tokens.css) reads that
// tokens.css does not declare (colors, radii, shadows, fonts, display type,
// card shape) must be declared by each css/skins/*.css, so a token added to
// the base without a value gets a failing test instead of a transparent
// badge. It runs with the package tests (make test, CI); the failure names
// the skin, the token and a base file that reads it.
func TestSkinsDeclareEveryBaseToken(t *testing.T) {
	files := staticFS(t)
	neutral := cssNames(varDecl, readStatic(t, "css/tokens.css"))
	readBy := map[string]string{}
	for _, base := range baseCSS(t) {
		for name := range cssNames(varRef, readStatic(t, base)) {
			if _, seen := readBy[name]; !seen && !neutral[name] && !runtimeVars[name] {
				readBy[name] = base
			}
		}
	}
	required := slices.Sorted(maps.Keys(readBy))
	if len(required) < 20 {
		t.Fatalf("the base stylesheets read only %d skin tokens; the var() pattern is broken", len(required))
	}
	skins, err := fs.Glob(files, "css/skins/*.css")
	if err != nil {
		t.Fatal(err)
	}
	if len(skins) == 0 {
		t.Fatal("found no skin under css/skins")
	}
	index := readStatic(t, "index.html")
	for _, skin := range skins {
		declared := cssNames(varDecl, readStatic(t, skin))
		for _, name := range required {
			if !declared[name] {
				t.Errorf("static/%s does not declare %s, which %s reads", skin, name, readBy[name])
			}
		}
		if !strings.Contains(index, `href="`+skin+`"`) {
			t.Errorf("index.html does not link %s", skin)
		}
		scope := `:root[data-skin="` + strings.TrimSuffix(path.Base(skin), ".css") + `"]`
		if !strings.Contains(readStatic(t, skin), scope) {
			t.Errorf("static/%s has no %s block; a skin is scoped by its file name", skin, scope)
		}
	}
}

var stylesheetLink = regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`)

// TestStylesheetsLinkedInOrder checks the cascade order index.html sets:
// tokens.css first, then every base stylesheet, then the skins, so a skin
// rule always follows the base rule it overrides. An embedded stylesheet
// that index.html does not link would ship without ever applying.
func TestStylesheetsLinkedInOrder(t *testing.T) {
	var links []string
	for _, m := range stylesheetLink.FindAllStringSubmatch(readStatic(t, "index.html"), -1) {
		links = append(links, m[1])
	}
	if len(links) == 0 || links[0] != "css/tokens.css" {
		t.Fatalf("index.html stylesheets = %v, want css/tokens.css first", links)
	}
	skins, err := fs.Glob(staticFS(t), "css/skins/*.css")
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]string{"css/tokens.css"}, baseCSS(t)...), skins...)
	for _, name := range all {
		if !slices.Contains(links, name) {
			t.Errorf("index.html does not link %s", name)
		}
	}
	firstSkin := slices.IndexFunc(links, func(l string) bool { return strings.HasPrefix(l, "css/skins/") })
	if firstSkin < 0 {
		t.Fatal("index.html links no skin")
	}
	for i, l := range links {
		if i > firstSkin && !strings.HasPrefix(l, "css/skins/") {
			t.Errorf("index.html links %s after the first skin; base stylesheets precede every skin", l)
		}
	}
}

var (
	settingsFieldKey = regexp.MustCompile(`\bkey:\s*'([a-z_]+\.[a-z0-9_]+)'`)
	notEditedBlock   = regexp.MustCompile(`(?s)export const NOT_EDITED = \{(.*?)\};`)
	notEditedEntry   = regexp.MustCompile(`'([a-z_]+\.[a-z0-9_]+)'\s*:\s*'([^']*)'`)
)

// TestSettingsFieldsCoverRuntimeConfig keeps the Settings page complete in
// both directions: every leaf of the RuntimeConfig schema in
// internal/api/openapi.yaml (what GET /api/v1/config returns and PUT accepts)
// has a field in static/js/lib/settings.js or an entry with a reason in its
// NOT_EDITED list, and no field or entry names a key the API does not return.
// A new runtime key therefore fails here until the page can edit it or says
// why it does not.
func TestSettingsFieldsCoverRuntimeConfig(t *testing.T) {
	spec, err := os.ReadFile("../../internal/api/openapi.yaml")
	if err != nil {
		t.Fatalf("read the API contract: %v", err)
	}
	type schema struct {
		Type       string            `yaml:"type"`
		Properties map[string]schema `yaml:"properties"`
	}
	var doc struct {
		Components struct {
			Schemas map[string]schema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	runtime, ok := doc.Components.Schemas["RuntimeConfig"]
	if !ok {
		t.Fatal("openapi.yaml has no RuntimeConfig schema")
	}
	api := map[string]bool{}
	for section, body := range runtime.Properties {
		if len(body.Properties) == 0 {
			t.Errorf("RuntimeConfig.%s has no properties; the settings guard expects section.key leaves", section)
		}
		for name, leaf := range body.Properties {
			if leaf.Type == "object" {
				t.Errorf("RuntimeConfig.%s.%s is an object; lib/settings.js addresses two-level keys only", section, name)
				continue
			}
			api[section+"."+name] = true
		}
	}
	if len(api) < 30 {
		t.Fatalf("RuntimeConfig has only %d leaves; the schema walk is broken", len(api))
	}

	src := readStatic(t, "js/lib/settings.js")
	fields := map[string]bool{}
	for _, m := range settingsFieldKey.FindAllStringSubmatch(src, -1) {
		if fields[m[1]] {
			t.Errorf("lib/settings.js has two fields for %s", m[1])
		}
		fields[m[1]] = true
	}
	if len(fields) < 30 {
		t.Fatalf("found only %d field keys in lib/settings.js; the key pattern is broken", len(fields))
	}
	block := notEditedBlock.FindStringSubmatch(src)
	if block == nil {
		t.Fatal("lib/settings.js has no `export const NOT_EDITED = {...};` list")
	}
	excluded := map[string]string{}
	for _, m := range notEditedEntry.FindAllStringSubmatch(block[1], -1) {
		excluded[m[1]] = m[2]
	}

	for _, key := range slices.Sorted(maps.Keys(api)) {
		_, skip := excluded[key]
		switch {
		case !fields[key] && !skip:
			t.Errorf("GET /api/v1/config returns %s, but lib/settings.js has no field for it and NOT_EDITED does not say why", key)
		case fields[key] && skip:
			t.Errorf("%s has a field in lib/settings.js and is also listed in NOT_EDITED", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		if !api[key] {
			t.Errorf("lib/settings.js has a field for %s, which RuntimeConfig in openapi.yaml does not have", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(excluded)) {
		if !api[key] {
			t.Errorf("NOT_EDITED lists %s, which RuntimeConfig in openapi.yaml does not have", key)
		}
		if strings.TrimSpace(excluded[key]) == "" {
			t.Errorf("NOT_EDITED lists %s without a reason", key)
		}
	}
}

func TestLoginWaitMatchesTheGoClient(t *testing.T) {
	m := regexp.MustCompile(`const LOGIN_TIMEOUT_MS = (\d+);`).FindStringSubmatch(readStatic(t, "js/api.js"))
	if m == nil {
		t.Fatal("LOGIN_TIMEOUT_MS not found in api.js")
	}
	ms, _ := strconv.Atoi(m[1]) // the pattern only matches digits
	if got := time.Duration(ms) * time.Millisecond; got != client.LoginWait {
		t.Fatalf("LOGIN_TIMEOUT_MS is %v, client.LoginWait %v: a login to a wrapper amdl runs outlasts the shorter one", got, client.LoginWait)
	}
}
