package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFileReadV2PreservesLegacyByteRangeAndHashes(t *testing.T) {
	raw := []byte("甲乙丙\n")
	path := writeFileReadFixture(t, raw)
	client := newFileReadClient(t)
	result, err := client.fileRead(context.Background(), map[string]any{"path": path, "offset": 0, "limit": 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content == nil || *result.Content != "甲" || result.Offset != 0 || result.BytesRead != 3 || result.SourceBytesRead != 3 || !result.Truncated {
		t.Fatalf("legacy byte result=%+v content=%v", result, result.Content)
	}
	if result.FileSHA256 != hashBytes(raw) || result.ChunkSHA256 != hashBytes([]byte("甲")) || result.Size != int64(len(raw)) || result.Encoding != "utf-8" {
		t.Fatalf("legacy hash/stat result=%+v", result)
	}
	second, err := client.fileRead(context.Background(), map[string]any{"path": path, "offset": 3, "limit": 3})
	if err != nil || second.Content == nil || *second.Content != "乙" || second.Offset != 3 {
		t.Fatalf("legacy offset result=%+v err=%v", second, err)
	}
}

func TestFileReadV2LineSelectorsAndRenderedHash(t *testing.T) {
	raw := []byte("one\ntwo\nthree\nfour\nfive")
	path := writeFileReadFixture(t, raw)
	client := newFileReadClient(t)
	tests := []struct {
		name      string
		params    map[string]any
		content   string
		lineStart int
		lineEnd   int
	}{
		{"range", map[string]any{"lineStart": 2, "lineCount": 2}, "two\nthree\n", 2, 3},
		{"head", map[string]any{"headLines": 2}, "one\ntwo\n", 1, 2},
		{"tail", map[string]any{"tailLines": 2}, "four\nfive", 4, 5},
		{"around", map[string]any{"aroundLine": 3, "contextLines": 1}, "two\nthree\nfour\n", 2, 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := map[string]any{"path": path}
			for key, value := range test.params {
				params[key] = value
			}
			result, err := client.fileRead(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			if result.Content == nil || *result.Content != test.content || result.LineStart != test.lineStart || result.LineEnd != test.lineEnd || !result.Truncated {
				t.Fatalf("line result=%+v content=%v", result, result.Content)
			}
			if result.ChunkSHA256 != hashBytes([]byte(test.content)) || result.FileSHA256 != hashBytes(raw) {
				t.Fatalf("line hashes=%+v", result)
			}
		})
	}
	rendered, err := client.fileRead(context.Background(), map[string]any{
		"path": path, "lineStart": 2, "lineCount": 2, "includeLineNumbers": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRendered := "2: two\n3: three\n"
	if rendered.Content == nil || *rendered.Content != wantRendered || rendered.BytesRead != int64(len(wantRendered)) || rendered.SourceBytesRead != int64(len("two\nthree\n")) {
		t.Fatalf("numbered result=%+v content=%v", rendered, rendered.Content)
	}
	if rendered.ChunkSHA256 != hashBytes([]byte(wantRendered)) || rendered.FileSHA256 != hashBytes(raw) {
		t.Fatalf("numbered hashes=%+v", rendered)
	}
}

func TestFileReadV2StatOnlyOmitsChunkAndReturnsOriginalHash(t *testing.T) {
	raw := []byte("stat me\n")
	path := writeFileReadFixture(t, raw)
	result, err := newFileReadClient(t).fileRead(context.Background(), map[string]any{"path": path, "statOnly": true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.StatOnly || result.Content != nil || result.ChunkSHA256 != "" || result.BytesRead != 0 || result.FileSHA256 != hashBytes(raw) || result.Size != int64(len(raw)) || result.Truncated {
		t.Fatalf("statOnly result=%+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"content"`) || strings.Contains(string(encoded), `"chunkSha256"`) {
		t.Fatalf("statOnly serialized a chunk: %s", encoded)
	}
}

func TestFileReadV2CRLFBOMNoFinalNewlineAndUTF8Boundary(t *testing.T) {
	client := newFileReadClient(t)
	crlf := []byte("one\r\ntwo\r\nthree")
	crlfResult, err := client.fileRead(context.Background(), map[string]any{"path": writeFileReadFixture(t, crlf), "headLines": 2})
	if err != nil || crlfResult.Content == nil || *crlfResult.Content != "one\r\ntwo\r\n" || crlfResult.LineEnd != 2 {
		t.Fatalf("CRLF result=%+v err=%v", crlfResult, err)
	}
	bom := append([]byte{0xef, 0xbb, 0xbf}, []byte("first\nlast")...)
	bomResult, err := client.fileRead(context.Background(), map[string]any{"path": writeFileReadFixture(t, bom), "lineStart": 1, "lineCount": 1})
	if err != nil || bomResult.Content == nil || *bomResult.Content != "\ufefffirst\n" || bomResult.FileSHA256 != hashBytes(bom) {
		t.Fatalf("BOM result=%+v err=%v", bomResult, err)
	}
	boundary := append([]byte(strings.Repeat("a", fileReadScanBufferSize-1)), []byte("界\nlast")...)
	boundaryResult, err := client.fileRead(context.Background(), map[string]any{"path": writeFileReadFixture(t, boundary), "tailLines": 1})
	if err != nil || boundaryResult.Content == nil || *boundaryResult.Content != "last" || boundaryResult.LineStart != 2 || boundaryResult.LineEnd != 2 {
		t.Fatalf("UTF-8 boundary/tail result=%+v err=%v", boundaryResult, err)
	}
}

func TestFileReadV2LongLineAndLargeTailStayBounded(t *testing.T) {
	longLine := strings.Repeat("界", maxFileReadBytes) + "\n"
	large := []byte(longLine + strings.Repeat("padding\n", 30000) + "final-no-newline")
	path := writeFileReadFixture(t, large)
	client := newFileReadClient(t)
	head, err := client.fileRead(context.Background(), map[string]any{"path": path, "headLines": 1})
	if err != nil {
		t.Fatal(err)
	}
	if head.Content == nil || len(*head.Content) > maxFileReadBytes || !utf8.ValidString(*head.Content) || !head.Truncated || head.LineStart != 1 || head.LineEnd != 1 {
		t.Fatalf("bounded long-line result=%+v contentBytes=%d", head, len(*head.Content))
	}
	tail, err := client.fileRead(context.Background(), map[string]any{"path": path, "tailLines": 1})
	if err != nil || tail.Content == nil || *tail.Content != "final-no-newline" || tail.LineEnd != 30002 || len(*tail.Content) > maxFileReadBytes {
		t.Fatalf("bounded large tail=%+v err=%v", tail, err)
	}
}

func TestFileReadV2RejectsAmbiguousAndOutOfRangeSelectors(t *testing.T) {
	path := writeFileReadFixture(t, []byte("one\ntwo\n"))
	client := newFileReadClient(t)
	invalid := []map[string]any{
		{"offset": 0, "headLines": 1},
		{"limit": 10, "lineStart": 1, "lineCount": 1},
		{"lineStart": 1},
		{"lineCount": 1},
		{"headLines": 1, "tailLines": 1},
		{"aroundLine": 1},
		{"contextLines": 0},
		{"aroundLine": 1, "contextLines": -1},
		{"statOnly": true, "tailLines": 1},
		{"statOnly": true, "includeLineNumbers": true},
		{"includeLineNumbers": true},
		{"headLines": 0},
		{"tailLines": maxFileReadLines + 1},
		{"lineStart": maxFileReadLineNumber + 1, "lineCount": 1},
	}
	for _, extra := range invalid {
		params := map[string]any{"path": path}
		for key, value := range extra {
			params[key] = value
		}
		if _, err := client.fileRead(context.Background(), params); err == nil {
			t.Fatalf("invalid file_read params accepted: %+v", extra)
		}
	}
}

func TestFileReadV2RejectsBinaryInvalidUTF8AndNonRegular(t *testing.T) {
	client := newFileReadClient(t)
	for _, raw := range [][]byte{{'o', 'k', 0, 'x'}, {'o', 'k', 0xff}} {
		if _, err := client.fileRead(context.Background(), map[string]any{"path": writeFileReadFixture(t, raw), "statOnly": true}); err == nil {
			t.Fatalf("statOnly accepted non-text bytes %v", raw)
		}
	}
	if _, err := client.fileRead(context.Background(), map[string]any{"path": t.TempDir(), "statOnly": true}); err == nil {
		t.Fatal("statOnly accepted a directory")
	}
}

func newFileReadClient(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{DataDir: t.TempDir(), Version: "file-read-test"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeFileReadFixture(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
