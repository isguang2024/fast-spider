package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	rgExe := flag.String("rg-exe", "", "prepared ripgrep executable to bundle")
	out := flag.String("out", "", "output component zip")
	flag.Parse()
	if err := run(*rgExe, *out); err != nil {
		fmt.Fprintln(os.Stderr, "ripgreppack:", err)
		os.Exit(1)
	}
}

func run(rgExe, out string) error {
	if strings.TrimSpace(rgExe) == "" {
		return errors.New("--rg-exe is required")
	}
	if strings.TrimSpace(out) == "" {
		return errors.New("--out is required")
	}

	info, err := os.Lstat(rgExe)
	if err != nil {
		return errors.New("ripgrep executable is unavailable")
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("ripgrep executable must be a non-empty regular file")
	}

	input, err := os.Open(rgExe)
	if err != nil {
		return errors.New("ripgrep executable cannot be opened")
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return errors.New("ripgrep executable changed during validation")
	}
	currentInfo, err := os.Lstat(rgExe)
	if err != nil || !currentInfo.Mode().IsRegular() || !os.SameFile(openedInfo, currentInfo) {
		return errors.New("ripgrep executable changed during validation")
	}
	if outputInfo, err := os.Lstat(out); err == nil {
		if !outputInfo.Mode().IsRegular() || os.SameFile(openedInfo, outputInfo) {
			return errors.New("output archive must be a distinct regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("output archive cannot be inspected")
	}

	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	output, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create component archive: %w", err)
	}

	zw := zip.NewWriter(output)
	closeArchive := func(base error) error {
		zipErr := zw.Close()
		fileErr := output.Close()
		if base != nil {
			_ = os.Remove(out)
			return base
		}
		if zipErr != nil {
			_ = os.Remove(out)
			return fmt.Errorf("finish component archive: %w", zipErr)
		}
		if fileErr != nil {
			_ = os.Remove(out)
			return fmt.Errorf("close component archive: %w", fileErr)
		}
		return nil
	}

	entryName := "rg"
	if strings.EqualFold(filepath.Ext(rgExe), ".exe") {
		entryName = "rg.exe"
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return closeArchive(fmt.Errorf("create archive header: %w", err))
	}
	header.Name = entryName
	header.Method = zip.Deflate
	entry, err := zw.CreateHeader(header)
	if err != nil {
		return closeArchive(fmt.Errorf("create archive entry: %w", err))
	}
	if _, err := io.Copy(entry, input); err != nil {
		return closeArchive(fmt.Errorf("write archive entry: %w", err))
	}
	return closeArchive(nil)
}
