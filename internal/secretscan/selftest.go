package secretscan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SelfTest verifies detection and redaction with a generated, non-production
// canary. The canary is never written to output or returned in an error.
func SelfTest(ctx context.Context) error {
	root, err := os.MkdirTemp("", "fast-spider-secretscan-selftest-")
	if err != nil {
		return errors.New("create self-test directory")
	}
	defer os.RemoveAll(root)
	canary := "sk-" + strings.Repeat("A7", 18)
	if err := os.WriteFile(filepath.Join(root, "fixture.bin"), append([]byte{0}, []byte(canary)...), 0o600); err != nil {
		return errors.New("write self-test fixture")
	}
	findings, err := ScanTree(ctx, root, Options{})
	if err != nil {
		return errors.New("scan self-test fixture")
	}
	if len(findings) == 0 {
		return errors.New("self-test canary was not detected")
	}
	var output bytes.Buffer
	if err := WriteFindings(&output, findings); err != nil {
		return errors.New("format self-test findings")
	}
	if bytes.Contains(output.Bytes(), []byte(canary)) {
		return errors.New("self-test output redaction failed")
	}
	return nil
}
