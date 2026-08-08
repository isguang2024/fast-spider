package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/isguang2024/fast-spider/internal/contractgen"
)

func main() {
	schemaPath := flag.String("schema", "contracts/v1/control.schema.json", "JSON Schema input")
	outPath := flag.String("out", "internal/protocol/v1/generated_types.go", "generated Go output")
	pkg := flag.String("package", "v1", "generated package name")
	flag.Parse()

	raw, err := os.ReadFile(*schemaPath)
	if err != nil {
		fatalf("read schema: %v", err)
	}
	generated, err := contractgen.Generate(raw, *pkg)
	if err != nil {
		fatalf("generate: %v", err)
	}
	if err := os.WriteFile(*outPath, generated, 0o644); err != nil {
		fatalf("write output: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
