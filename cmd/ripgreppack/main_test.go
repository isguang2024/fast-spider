package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunRequiresArguments(t *testing.T) {
	t.Parallel()
	if err := run("", "out.zip"); err == nil || !strings.Contains(err.Error(), "--rg-exe") {
		t.Fatalf("run without --rg-exe error = %v", err)
	}
	if err := run("rg", ""); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("run without --out error = %v", err)
	}
}

func TestRunRejectsNonRegularAndEmptyInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := run(root, filepath.Join(root, "directory.zip")); err == nil {
		t.Fatal("run accepted a directory")
	}
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(empty, filepath.Join(root, "empty.zip")); err == nil {
		t.Fatal("run accepted an empty file")
	}
}

func TestRunRejectsSymbolicLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "rg-target")
	if err := os.WriteFile(target, []byte("ripgrep"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "rg-link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires Windows developer mode or privilege: %v", err)
		}
		t.Fatal(err)
	}
	if err := run(link, filepath.Join(root, "linked.zip")); err == nil {
		t.Fatal("run accepted a symbolic link")
	}
}

func TestRunRejectsOutputAliasingInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := filepath.Join(root, "rg.exe")
	content := []byte("ripgrep")
	if err := os.WriteFile(input, content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(input, input); err == nil {
		t.Fatal("run accepted the input file as its output archive")
	}
	got, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("input changed to %q", got)
	}

	outputLink := filepath.Join(root, "component.zip")
	if err := os.Symlink(input, outputLink); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires Windows developer mode or privilege: %v", err)
		}
		t.Fatal(err)
	}
	if err := run(input, outputLink); err == nil {
		t.Fatal("run accepted a symbolic link as its output archive")
	}
}

func TestRunCreatesSingleSafeRootEntry(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		inputName string
		entryName string
	}{
		{name: "windows", inputName: "prepared-rg.exe", entryName: "rg.exe"},
		{name: "other", inputName: "prepared-rg", entryName: "rg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			input := filepath.Join(root, "trusted", test.inputName)
			if err := os.MkdirAll(filepath.Dir(input), 0o700); err != nil {
				t.Fatal(err)
			}
			content := []byte("prepared ripgrep executable\n")
			if err := os.WriteFile(input, content, 0o751); err != nil {
				t.Fatal(err)
			}
			inputInfo, err := os.Stat(input)
			if err != nil {
				t.Fatal(err)
			}

			out := filepath.Join(root, "nested", "component.zip")
			if err := run(input, out); err != nil {
				t.Fatal(err)
			}
			archive, err := zip.OpenReader(out)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			if len(archive.File) != 1 {
				t.Fatalf("archive entries = %d, want 1", len(archive.File))
			}
			entry := archive.File[0]
			if entry.Name != test.entryName {
				t.Fatalf("entry name = %q, want %q", entry.Name, test.entryName)
			}
			if filepath.IsAbs(entry.Name) || strings.Contains(entry.Name, "/") || strings.Contains(entry.Name, "\\") || strings.Contains(entry.Name, "..") {
				t.Fatalf("unsafe archive entry name %q", entry.Name)
			}
			if entry.Mode().Perm() != inputInfo.Mode().Perm() {
				t.Fatalf("entry mode = %v, want %v", entry.Mode().Perm(), inputInfo.Mode().Perm())
			}
			reader, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(reader)
			closeErr := reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if string(got) != string(content) {
				t.Fatalf("entry content = %q, want %q", got, content)
			}
		})
	}
}
