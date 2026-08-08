package contractgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedProtocolTypesAreCurrent(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "contracts", "v1", "control.schema.json")
	generatedPath := filepath.Join("..", "protocol", "v1", "generated_types.go")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Generate(schema, "v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated_types.go is stale; regenerate it from contracts/v1/control.schema.json")
	}
}
