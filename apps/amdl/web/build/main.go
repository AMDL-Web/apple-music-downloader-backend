// Command build makes the browser demo of amdl's TUI: apps/amdl/web, the TUI
// over the in-memory demo backend, compiled to WebAssembly, in a page whose
// terminal is xterm.js.
//
//	go run ./apps/amdl/web/build -out apps/amdl/build/web
//
// bubbletea, and the clipboard package bubbles pulls in, have files per OS
// and none for js. The build copies those two modules out of the module
// cache, adds the files in shim/, and compiles against the copies through a
// temporary go.mod. A shim that no longer fits a new release fails the
// compile, and with it the build; so does a release that brings its own js
// file under the shim's name.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed shim page
var assets embed.FS

// shims maps a module to the files added to it, from shim/<file>.txt.
var shims = map[string]map[string]string{
	"charm.land/bubbletea/v2":     {"tty_js.go": "bubbletea_tty_js.go.txt"},
	"github.com/atotto/clipboard": {"clipboard_js.go": "clipboard_js.go.txt"},
}

// xterm is the terminal the page runs and its WebGL renderer, fetched from
// npm into xterm/ and checked against the integrity npm publishes for each
// (npm view <name>@<version> dist.integrity). files maps a path in the
// package's tarball to its name in xterm/.
var xterm = []struct {
	name, version, integrity string
	files                    map[string]string
}{
	{"@xterm/xterm", "5.5.0", "sha512-hqJHYaQb5OptNunnyAnkHyM8aCjZ1MEIDTQu1iIbbTD/xops91NB5yq1ZK/dC2JDbVWtF23zUtl9JE2NqwT87A==",
		map[string]string{"package/lib/xterm.js": "xterm.js", "package/css/xterm.css": "xterm.css", "package/LICENSE": "LICENSE"}},
	{"@xterm/addon-webgl", "0.18.0", "sha512-xCnfMBTI+/HKPdRnSOHaJDRqEpq2Ugy8LEj9GiY4J3zJObo3joylIFaMvzBwbYRg8zLtkO0KQaStCeSfoaI2/w==",
		map[string]string{"package/lib/addon-webgl.js": "addon-webgl.js", "package/LICENSE": "LICENSE-addon-webgl"}},
}

func main() {
	out := flag.String("out", "", "directory to write the page into (required)")
	ldflags := flag.String("ldflags", "-s -w", "linker flags for the wasm build")
	flag.Parse()
	if *out == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./apps/amdl/web/build -out <dir> [-ldflags <flags>]")
		os.Exit(2)
	}
	if err := build(*out, *ldflags); err != nil {
		fmt.Fprintln(os.Stderr, "tui web build:", err)
		os.Exit(1)
	}
}

func build(out, ldflags string) error {
	root, err := goEnv("GOMOD")
	if err != nil {
		return err
	}
	root = filepath.Dir(root)
	tmp, err := os.MkdirTemp("", "amdl-tui-web-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	for path, files := range shims {
		dir, err := patched(root, tmp, path, files)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		mod = fmt.Appendf(mod, "\nreplace %s => %s\n", path, dir)
	}
	modfile := filepath.Join(tmp, "web.mod")
	if err := os.WriteFile(modfile, mod, 0o644); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(root, "go.sum"), filepath.Join(tmp, "web.sum")); err != nil {
		return err
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-modfile", modfile, "-trimpath", "-ldflags", ldflags,
		"-o", filepath.Join(out, "amdl.wasm"), "./apps/amdl/web")
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "GOWORK=off")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compiling apps/amdl/web for js/wasm (a shim in apps/amdl/web/build/shim may no longer fit): %w", err)
	}

	goroot, err := goEnv("GOROOT")
	if err != nil {
		return err
	}
	if err := copyFile(filepath.Join(goroot, "lib", "wasm", "wasm_exec.js"), filepath.Join(out, "wasm_exec.js")); err != nil {
		return err
	}
	for _, pkg := range xterm {
		if err := fetchNPM(filepath.Join(out, "xterm"), pkg.name, pkg.version, pkg.integrity, pkg.files); err != nil {
			return fmt.Errorf("%s %s: %w", pkg.name, pkg.version, err)
		}
	}
	page, err := fs.ReadDir(assets, "page")
	if err != nil {
		return err
	}
	for _, f := range page {
		if f.Name() == "index.html" {
			continue
		}
		body, err := assets.ReadFile("page/" + f.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, f.Name()), body, 0o644); err != nil {
			return err
		}
	}
	if err := writeIndex(out); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(out, "amdl.wasm"))
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (amdl.wasm %.1f MB)\n", out, float64(info.Size())/1e6)
	return nil
}

// versioned are the files index.html names, which it names with a hash of
// their content: the CDN in front of the website keeps scripts and
// stylesheets for hours, and wasm_exec.js has to come from the Go that built
// amdl.wasm.
var versioned = []string{"amdl.wasm", "wasm_exec.js", "app.js", "app.css", "xterm/xterm.js", "xterm/addon-webgl.js", "xterm/xterm.css"}

func writeIndex(out string) error {
	index, err := assets.ReadFile("page/index.html")
	if err != nil {
		return err
	}
	for _, name := range versioned {
		body, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		ref := []byte(`"` + name + `"`)
		if bytes.Count(index, ref) != 1 {
			return fmt.Errorf("page/index.html names %s %d times, want once", name, bytes.Count(index, ref))
		}
		index = bytes.Replace(index, ref, []byte(`"`+name+"?v="+hex.EncodeToString(sum[:6])+`"`), 1)
	}
	return os.WriteFile(filepath.Join(out, "index.html"), index, 0o644)
}

// patched copies module path, at the version go.mod builds with, into tmp
// and adds its shim files, refusing one the module already has.
func patched(root, tmp, path string, files map[string]string) (string, error) {
	cmd := exec.Command("go", "mod", "download", "-json", path)
	cmd.Dir, cmd.Stderr = root, os.Stderr
	cmd.Env = append(os.Environ(), "GOWORK=off")
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	var m struct{ Dir, Version string }
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	dst := filepath.Join(tmp, strings.NewReplacer("/", "_", ".", "_").Replace(path))
	if err := os.CopyFS(dst, os.DirFS(m.Dir)); err != nil {
		return "", err
	}
	for name, src := range files {
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			return "", fmt.Errorf("%s %s has its own %s; drop the shim for it", path, m.Version, name)
		}
		body, err := assets.ReadFile("shim/" + src)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, name), body, 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// fetchNPM writes files of the npm package name@version into dir.
func fetchNPM(dir, name, version, integrity string, files map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base := name[strings.LastIndex(name, "/")+1:]
	url := fmt.Sprintf("https://registry.npmjs.org/%s/-/%s-%s.tgz", name, base, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tgz, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	sum := sha512.Sum512(tgz)
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != integrity {
		return fmt.Errorf("integrity %s, want %s", got, integrity)
	}
	want := maps.Clone(files)
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name, ok := want[h.Name]
		if !ok {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
		delete(want, h.Name)
	}
	if len(want) > 0 {
		return fmt.Errorf("the package lacks %v", want)
	}
	return nil
}

func goEnv(key string) (string, error) {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w", key, err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" || v == os.DevNull {
		return "", fmt.Errorf("go env %s is empty: run inside the module", key)
	}
	return v, nil
}

func copyFile(src, dst string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o644)
}
