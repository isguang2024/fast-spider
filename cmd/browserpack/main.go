package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type browserCatalog struct {
	Browsers []struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
	} `json:"browsers"`
}

func main() {
	sidecarDir := flag.String("sidecar-dir", "sidecar/browser", "browser sidecar directory")
	nodeExe := flag.String("node-exe", "", "Node.js executable to bundle")
	browsersDir := flag.String("browsers-dir", "", "Playwright browser cache root")
	out := flag.String("out", "", "output component zip")
	flag.Parse()
	if err := run(*sidecarDir, *nodeExe, *browsersDir, *out); err != nil {
		fmt.Fprintln(os.Stderr, "browserpack:", err)
		os.Exit(1)
	}
}

func run(sidecarDir, nodeExe, browsersDir, out string) error {
	if strings.TrimSpace(out) == "" {
		return errors.New("--out is required")
	}
	if strings.TrimSpace(nodeExe) == "" {
		return errors.New("--node-exe is required")
	}
	if strings.TrimSpace(browsersDir) == "" {
		return errors.New("--browsers-dir is required")
	}
	for _, required := range []string{"package.json", "index.mjs", filepath.Join("node_modules", "playwright", "package.json"), filepath.Join("node_modules", "playwright-core", "browsers.json")} {
		if info, err := os.Stat(filepath.Join(sidecarDir, required)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("sidecar is missing %s", required)
		}
	}
	if info, err := os.Stat(nodeExe); err != nil || !info.Mode().IsRegular() {
		return errors.New("bundled Node.js executable is unavailable")
	}

	catalogRaw, err := os.ReadFile(filepath.Join(sidecarDir, "node_modules", "playwright-core", "browsers.json"))
	if err != nil {
		return err
	}
	var catalog browserCatalog
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		return fmt.Errorf("decode Playwright browser catalog: %w", err)
	}
	revisions := map[string]string{}
	for _, browser := range catalog.Browsers {
		switch browser.Name {
		case "chromium", "chromium-headless-shell", "ffmpeg":
			revisions[browser.Name] = browser.Revision
		}
	}
	for _, name := range []string{"chromium", "chromium-headless-shell", "ffmpeg"} {
		if revisions[name] == "" {
			return fmt.Errorf("Playwright catalog omitted %s", name)
		}
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(file)
	closeWithErr := func(base error) error {
		zipErr := zw.Close()
		fileErr := file.Close()
		if base != nil {
			return base
		}
		if zipErr != nil {
			return zipErr
		}
		return fileErr
	}

	if err := addTree(zw, sidecarDir, sidecarDir, func(rel string) bool {
		return rel == "package.json" || rel == "index.mjs" || rel == "node_modules" || strings.HasPrefix(rel, "node_modules/")
	}); err != nil {
		return closeWithErr(err)
	}
	nodeName := "node"
	if strings.EqualFold(filepath.Ext(nodeExe), ".exe") {
		nodeName = "node.exe"
	}
	if err := addFile(zw, nodeExe, nodeName); err != nil {
		return closeWithErr(err)
	}
	for _, name := range []string{"chromium", "chromium-headless-shell", "ffmpeg"} {
		folder := strings.ReplaceAll(name, "-", "_") + "-" + revisions[name]
		source := filepath.Join(browsersDir, folder)
		if info, err := os.Stat(source); err != nil || !info.IsDir() {
			return closeWithErr(fmt.Errorf("Playwright browser cache is missing %s", source))
		}
		if err := addTree(zw, source, source, func(string) bool { return true }, filepath.ToSlash(filepath.Join("browsers", folder))); err != nil {
			return closeWithErr(err)
		}
	}
	return closeWithErr(nil)
}

func addTree(zw *zip.Writer, root, current string, include func(string) bool, prefixes ...string) error {
	prefix := ""
	if len(prefixes) > 0 {
		prefix = strings.Trim(strings.ReplaceAll(prefixes[0], "\\", "/"), "/")
	}
	return filepath.Walk(current, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if !include(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported component file type: %s", path)
		}
		name := rel
		if prefix != "" {
			name = prefix + "/" + rel
		}
		return addFile(zw, path, name)
	})
}

func addFile(zw *zip.Writer, source, name string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = strings.TrimLeft(filepath.ToSlash(name), "/")
	header.Method = zip.Deflate
	output, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return err
}
