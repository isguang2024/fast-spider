package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxFileReadLines        = 2000
	maxFileReadContextLines = 1000
	maxFileReadLineNumber   = 10_000_000
	fileReadScanBufferSize  = 64 << 10
)

type fileReadSelection int

const (
	fileReadBytes fileReadSelection = iota
	fileReadLineRange
	fileReadTail
	fileReadStat
)

type normalizedFileRead struct {
	params    fileReadParams
	selection fileReadSelection
	lineStart int
	lineCount int
}

func (c *Client) fileReadV2(ctx context.Context, params map[string]any) (fileReadResult, error) {
	if err := ctx.Err(); err != nil {
		return fileReadResult{}, err
	}
	input, err := normalizeFileRead(params)
	if err != nil {
		return fileReadResult{}, err
	}
	target, err := ResolveMachinePath(input.params.Path)
	if err != nil {
		return fileReadResult{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return fileReadResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileReadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return fileReadResult{}, ErrNotRegularFile
	}
	scan, err := scanFileRead(ctx, file, info.Size(), input)
	if err != nil {
		return fileReadResult{}, err
	}
	result := fileReadResult{
		Path: filepath.Clean(target), Size: info.Size(), FileSHA256: scan.fileSHA256, Encoding: "utf-8",
	}
	if input.selection == fileReadStat {
		result.StatOnly = true
		return result, nil
	}
	raw, err := validateReturnedUTF8(scan.content, scan.bounded || scan.endOffset < info.Size() || scan.startOffset+int64(len(scan.content)) < scan.endOffset)
	if err != nil {
		return fileReadResult{}, err
	}
	if input.selection == fileReadBytes {
		content := string(raw)
		result.Content = &content
		result.Offset = scan.startOffset
		result.BytesRead = int64(len(raw))
		result.SourceBytesRead = int64(len(raw))
		result.Truncated = scan.startOffset > 0 || scan.endOffset < info.Size() || scan.bounded
		result.ChunkSHA256 = hashBytes(raw)
		return result, nil
	}
	contentBytes := raw
	sourceBytes := len(raw)
	renderTruncated := false
	if input.params.IncludeLineNumbers && scan.lineStart > 0 {
		contentBytes, sourceBytes, renderTruncated = renderNumberedLines(raw, scan.lineStart, maxFileReadBytes)
	}
	content := string(contentBytes)
	result.Content = &content
	result.Offset = scan.startOffset
	result.BytesRead = int64(len(contentBytes))
	result.SourceBytesRead = int64(sourceBytes)
	result.LineStart = scan.lineStart
	result.LineEnd = returnedLineEnd(raw[:sourceBytes], scan.lineStart)
	result.Truncated = scan.startOffset > 0 || scan.endOffset < info.Size() || scan.bounded || renderTruncated || sourceBytes < len(raw)
	result.ChunkSHA256 = hashBytes(contentBytes)
	return result, nil
}

type fileReadScan struct {
	fileSHA256  string
	content     []byte
	startOffset int64
	endOffset   int64
	lineStart   int
	bounded     bool
}

func scanFileRead(ctx context.Context, file *os.File, size int64, input normalizedFileRead) (fileReadScan, error) {
	result := fileReadScan{endOffset: size}
	hasher := sha256.New()
	buffer := make([]byte, fileReadScanBufferSize)
	pending := make([]byte, 0, utf8.UTFMax)
	selected := make([]byte, 0, maxFileReadBytes+1)
	tailWindow := make([]byte, 0, maxFileReadBytes+1)
	currentLine := 1
	lastWasNewline := false
	if input.selection == fileReadBytes {
		result.startOffset = input.params.Offset
		if result.startOffset > size {
			result.startOffset = size
		}
		result.endOffset = result.startOffset + input.params.Limit
		if result.endOffset > size {
			result.endOffset = size
		}
	} else if input.selection == fileReadLineRange {
		result.startOffset = -1
		if input.lineStart == 1 && size > 0 {
			result.startOffset = 0
			result.lineStart = 1
		}
	}
	var offset int64
	for offset < size {
		if err := ctx.Err(); err != nil {
			return fileReadScan{}, err
		}
		toRead := len(buffer)
		if remaining := size - offset; remaining < int64(toRead) {
			toRead = int(remaining)
		}
		n, err := file.ReadAt(buffer[:toRead], offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return fileReadScan{}, err
		}
		if n == 0 {
			return fileReadScan{}, io.ErrUnexpectedEOF
		}
		chunk := buffer[:n]
		if bytes.IndexByte(chunk, 0) >= 0 {
			return fileReadScan{}, ErrBinaryOrInvalidUTF8
		}
		_, _ = hasher.Write(chunk)
		combined := make([]byte, 0, len(pending)+len(chunk))
		combined = append(combined, pending...)
		combined = append(combined, chunk...)
		last := offset+int64(n) >= size
		validPrefix, suffix, ok := splitValidUTF8Chunk(combined, last)
		if !ok || !utf8.Valid(validPrefix) {
			return fileReadScan{}, ErrBinaryOrInvalidUTF8
		}
		pending = append(pending[:0], suffix...)

		switch input.selection {
		case fileReadBytes:
			chunkStart, chunkEnd := offset, offset+int64(n)
			start, end := maxInt64(chunkStart, result.startOffset), minInt64(chunkEnd, result.endOffset)
			if start < end {
				selected = append(selected, chunk[start-chunkStart:end-chunkStart]...)
			}
		case fileReadLineRange:
			lastRequested := input.lineStart + input.lineCount - 1
			for index, value := range chunk {
				absolute := offset + int64(index)
				if currentLine >= input.lineStart && currentLine <= lastRequested {
					if len(selected) <= maxFileReadBytes {
						selected = append(selected, value)
					}
				}
				lastWasNewline = value == '\n'
				if value != '\n' {
					continue
				}
				if currentLine == lastRequested {
					result.endOffset = absolute + 1
				}
				currentLine++
				if currentLine == input.lineStart && absolute+1 < size {
					result.startOffset = absolute + 1
					result.lineStart = input.lineStart
				}
			}
		case fileReadTail:
			tailWindow = append(tailWindow, chunk...)
			if len(tailWindow) > maxFileReadBytes+1 {
				tailWindow = append([]byte(nil), tailWindow[len(tailWindow)-(maxFileReadBytes+1):]...)
				result.bounded = true
			}
			currentLine += bytes.Count(chunk, []byte{'\n'})
			lastWasNewline = chunk[len(chunk)-1] == '\n'
		}
		offset += int64(n)
	}
	if len(pending) != 0 {
		return fileReadScan{}, ErrBinaryOrInvalidUTF8
	}
	result.fileSHA256 = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if input.selection == fileReadStat {
		return result, nil
	}
	if input.selection == fileReadTail {
		return finishTailScan(result, tailWindow, size, currentLine, lastWasNewline, input.lineCount), nil
	}
	if input.selection == fileReadLineRange && (result.startOffset < 0 || result.lineStart == 0) {
		result.startOffset, result.endOffset, selected = size, size, nil
	}
	if len(selected) > maxFileReadBytes {
		selected = selected[:maxFileReadBytes]
		result.bounded = true
	}
	result.content = selected
	return result, nil
}

func finishTailScan(result fileReadScan, window []byte, size int64, totalLines int, lastWasNewline bool, requested int) fileReadScan {
	if size == 0 {
		result.content, result.startOffset, result.endOffset = []byte{}, 0, 0
		return result
	}
	if lastWasNewline {
		totalLines--
	}
	windowStart := size - int64(len(window))
	if windowStart > 0 {
		// The bounded tail window may begin in the middle of a UTF-8 rune. The
		// complete file was already validated during the same scan, so discard
		// only leading continuation bytes before selecting line boundaries.
		trimmed := 0
		for trimmed < len(window) && trimmed < utf8.UTFMax && !utf8.RuneStart(window[trimmed]) {
			trimmed++
		}
		if trimmed > 0 {
			window = window[trimmed:]
			windowStart += int64(trimmed)
			result.bounded = true
		}
	}
	starts := []int{0}
	if windowStart > 0 {
		starts = starts[:0]
		if newline := bytes.IndexByte(window, '\n'); newline >= 0 && newline+1 < len(window) {
			starts = append(starts, newline+1)
		}
	}
	for index, value := range window {
		if value == '\n' && index+1 < len(window) && (len(starts) == 0 || starts[len(starts)-1] != index+1) {
			starts = append(starts, index+1)
		}
	}
	if len(starts) == 0 {
		starts = []int{0}
		result.bounded = result.bounded || windowStart > 0
	}
	startIndex := 0
	if len(starts) > requested {
		startIndex = len(starts) - requested
	}
	contentStart := starts[startIndex]
	result.startOffset = windowStart + int64(contentStart)
	result.endOffset = size
	result.lineStart = totalLines - (len(starts) - startIndex) + 1
	if result.lineStart < 1 {
		result.lineStart = 1
	}
	result.content = append([]byte(nil), window[contentStart:]...)
	if len(result.content) > maxFileReadBytes {
		// Tail selectors must keep the end of the file. If one logical line is
		// larger than the response budget, drop its prefix and advance to a UTF-8
		// rune boundary instead of truncating the EOF side.
		trimmed := len(result.content) - maxFileReadBytes
		for trimmed < len(result.content) && !utf8.RuneStart(result.content[trimmed]) {
			trimmed++
		}
		result.content = append([]byte(nil), result.content[trimmed:]...)
		result.startOffset += int64(trimmed)
		result.bounded = true
	}
	return result
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func normalizeFileRead(params map[string]any) (normalizedFileRead, error) {
	var input fileReadParams
	if err := decodeParams(params, &input); err != nil {
		return normalizedFileRead{}, fmt.Errorf("invalid params: %w", err)
	}
	input.Path = strings.TrimSpace(input.Path)
	if input.Path == "" {
		return normalizedFileRead{}, fmt.Errorf("path is required")
	}
	hasOffset, hasLimit := fileReadParamPresent(params, "offset"), fileReadParamPresent(params, "limit")
	hasLineStart, hasLineCount := fileReadParamPresent(params, "lineStart"), fileReadParamPresent(params, "lineCount")
	hasHead, hasTail := fileReadParamPresent(params, "headLines"), fileReadParamPresent(params, "tailLines")
	hasAround, hasContext := fileReadParamPresent(params, "aroundLine"), fileReadParamPresent(params, "contextLines")
	lineSelectors := 0
	if hasLineStart || hasLineCount {
		lineSelectors++
	}
	if hasHead {
		lineSelectors++
	}
	if hasTail {
		lineSelectors++
	}
	if hasAround || hasContext {
		lineSelectors++
	}
	if input.StatOnly {
		if hasOffset || hasLimit || lineSelectors > 0 || input.IncludeLineNumbers {
			return normalizedFileRead{}, fmt.Errorf("statOnly cannot be combined with content selectors or includeLineNumbers")
		}
		return normalizedFileRead{params: input, selection: fileReadStat}, nil
	}
	if lineSelectors > 1 {
		return normalizedFileRead{}, fmt.Errorf("line selectors are mutually exclusive")
	}
	if lineSelectors > 0 && (hasOffset || hasLimit) {
		return normalizedFileRead{}, fmt.Errorf("byte range and line selectors cannot be combined")
	}
	if lineSelectors == 0 {
		if input.IncludeLineNumbers {
			return normalizedFileRead{}, fmt.Errorf("includeLineNumbers requires a line selector")
		}
		if input.Offset < 0 || input.Limit < 0 {
			return normalizedFileRead{}, fmt.Errorf("offset and limit must be non-negative")
		}
		if input.Limit == 0 {
			input.Limit = maxFileReadBytes
		}
		if input.Limit > maxFileReadBytes {
			return normalizedFileRead{}, ErrReadLimit
		}
		return normalizedFileRead{params: input, selection: fileReadBytes}, nil
	}
	if (hasLineStart || hasLineCount) && (!hasLineStart || !hasLineCount || input.LineStart < 1 || input.LineStart > maxFileReadLineNumber || input.LineCount < 1 || input.LineCount > maxFileReadLines) {
		return normalizedFileRead{}, fmt.Errorf("lineStart and lineCount are required together and must be within bounds")
	}
	if hasHead && (input.HeadLines < 1 || input.HeadLines > maxFileReadLines) {
		return normalizedFileRead{}, fmt.Errorf("headLines must be between 1 and %d", maxFileReadLines)
	}
	if hasTail && (input.TailLines < 1 || input.TailLines > maxFileReadLines) {
		return normalizedFileRead{}, fmt.Errorf("tailLines must be between 1 and %d", maxFileReadLines)
	}
	if (hasAround || hasContext) && (!hasAround || !hasContext || input.AroundLine < 1 || input.AroundLine > maxFileReadLineNumber || input.ContextLines < 0 || input.ContextLines > maxFileReadContextLines) {
		return normalizedFileRead{}, fmt.Errorf("aroundLine and contextLines are required together and must be within bounds")
	}
	switch {
	case hasLineStart:
		return normalizedFileRead{params: input, selection: fileReadLineRange, lineStart: input.LineStart, lineCount: input.LineCount}, nil
	case hasHead:
		return normalizedFileRead{params: input, selection: fileReadLineRange, lineStart: 1, lineCount: input.HeadLines}, nil
	case hasTail:
		return normalizedFileRead{params: input, selection: fileReadTail, lineCount: input.TailLines}, nil
	default:
		start := input.AroundLine - input.ContextLines
		if start < 1 {
			start = 1
		}
		end := input.AroundLine + input.ContextLines
		return normalizedFileRead{params: input, selection: fileReadLineRange, lineStart: start, lineCount: end - start + 1}, nil
	}
}

func fileReadParamPresent(params map[string]any, key string) bool {
	_, ok := params[key]
	return ok
}

func hashAndValidateTextFile(ctx context.Context, file *os.File, size int64) (string, error) {
	hasher := sha256.New()
	buffer := make([]byte, fileReadScanBufferSize)
	pending := make([]byte, 0, utf8.UTFMax)
	var offset int64
	for offset < size {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		toRead := len(buffer)
		if remaining := size - offset; remaining < int64(toRead) {
			toRead = int(remaining)
		}
		n, err := file.ReadAt(buffer[:toRead], offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if n == 0 {
			return "", io.ErrUnexpectedEOF
		}
		chunk := buffer[:n]
		if bytes.IndexByte(chunk, 0) >= 0 {
			return "", ErrBinaryOrInvalidUTF8
		}
		_, _ = hasher.Write(chunk)
		combined := make([]byte, 0, len(pending)+len(chunk))
		combined = append(combined, pending...)
		combined = append(combined, chunk...)
		last := offset+int64(n) >= size
		validPrefix, suffix, ok := splitValidUTF8Chunk(combined, last)
		if !ok || !utf8.Valid(validPrefix) {
			return "", ErrBinaryOrInvalidUTF8
		}
		pending = append(pending[:0], suffix...)
		offset += int64(n)
	}
	if len(pending) != 0 {
		return "", ErrBinaryOrInvalidUTF8
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func splitValidUTF8Chunk(input []byte, last bool) ([]byte, []byte, bool) {
	if utf8.Valid(input) {
		return input, nil, true
	}
	if last {
		return nil, nil, false
	}
	for suffixLength := 1; suffixLength < utf8.UTFMax && suffixLength <= len(input); suffixLength++ {
		prefix := input[:len(input)-suffixLength]
		if utf8.Valid(prefix) {
			return prefix, input[len(input)-suffixLength:], true
		}
	}
	return nil, nil, false
}

func readFileByteSelection(file *os.File, size int64, input fileReadParams, result fileReadResult) (fileReadResult, error) {
	offset := input.Offset
	if offset > size {
		offset = size
	}
	buf, bounded, err := readFileRange(file, offset, size, input.Limit)
	if err != nil {
		return fileReadResult{}, err
	}
	buf, err = validateReturnedUTF8(buf, bounded || offset+int64(len(buf)) < size)
	if err != nil {
		return fileReadResult{}, err
	}
	content := string(buf)
	result.Content = &content
	result.Offset = offset
	result.BytesRead = int64(len(buf))
	result.SourceBytesRead = int64(len(buf))
	result.Truncated = bounded || offset+int64(len(buf)) < size
	result.ChunkSHA256 = hashBytes(buf)
	return result, nil
}

func readFileRange(file *os.File, start, end, limit int64) ([]byte, bool, error) {
	if start < 0 || end < start || limit < 0 {
		return nil, false, fmt.Errorf("invalid file read range")
	}
	wanted := end - start
	readSize := wanted
	if readSize > limit+1 {
		readSize = limit + 1
	}
	if readSize == 0 {
		return []byte{}, false, nil
	}
	buf := make([]byte, int(readSize))
	n, err := file.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	buf = buf[:n]
	bounded := int64(len(buf)) > limit || wanted > limit
	if int64(len(buf)) > limit {
		buf = buf[:limit]
	}
	return buf, bounded, nil
}

func validateReturnedUTF8(input []byte, mayEndMidRune bool) ([]byte, error) {
	if utf8.Valid(input) {
		return input, nil
	}
	if mayEndMidRune {
		if trimmed, ok := trimIncompleteUTF8Suffix(input); ok {
			return trimmed, nil
		}
	}
	return nil, ErrBinaryOrInvalidUTF8
}

func locateForwardLines(ctx context.Context, file *os.File, size int64, requestedStart, requestedCount int) (int64, int64, int, int, error) {
	if size == 0 {
		return 0, 0, 0, 0, nil
	}
	buffer := make([]byte, fileReadScanBufferSize)
	currentLine := 1
	startOffset := int64(-1)
	if requestedStart == 1 {
		startOffset = 0
	}
	var offset int64
	lastWasNewline := false
	for offset < size {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, 0, err
		}
		toRead := len(buffer)
		if remaining := size - offset; remaining < int64(toRead) {
			toRead = int(remaining)
		}
		n, err := file.ReadAt(buffer[:toRead], offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, 0, 0, 0, err
		}
		for index, value := range buffer[:n] {
			absolute := offset + int64(index)
			lastWasNewline = value == '\n'
			if value != '\n' {
				continue
			}
			if startOffset >= 0 && currentLine >= requestedStart+requestedCount-1 {
				return startOffset, absolute + 1, requestedStart, currentLine, nil
			}
			currentLine++
			if currentLine == requestedStart {
				startOffset = absolute + 1
			}
		}
		offset += int64(n)
	}
	actualLastLine := currentLine
	if lastWasNewline {
		actualLastLine--
	}
	if startOffset < 0 || startOffset >= size || requestedStart > actualLastLine {
		return size, size, 0, 0, nil
	}
	endLine := requestedStart + requestedCount - 1
	if endLine > actualLastLine {
		endLine = actualLastLine
	}
	return startOffset, size, requestedStart, endLine, nil
}

func locateTailLines(ctx context.Context, file *os.File, size int64, requestedCount int) (int64, int64, int, int, error) {
	if size == 0 {
		return 0, 0, 0, 0, nil
	}
	starts := make([]int64, requestedCount)
	starts[0] = 0
	totalLines := 1
	buffer := make([]byte, fileReadScanBufferSize)
	var offset int64
	for offset < size {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, 0, err
		}
		toRead := len(buffer)
		if remaining := size - offset; remaining < int64(toRead) {
			toRead = int(remaining)
		}
		n, err := file.ReadAt(buffer[:toRead], offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, 0, 0, 0, err
		}
		for index, value := range buffer[:n] {
			if value != '\n' {
				continue
			}
			nextStart := offset + int64(index) + 1
			if nextStart >= size {
				continue
			}
			starts[totalLines%requestedCount] = nextStart
			totalLines++
		}
		offset += int64(n)
	}
	count := requestedCount
	if totalLines < count {
		count = totalLines
	}
	oldest := 0
	if totalLines > requestedCount {
		oldest = totalLines % requestedCount
	}
	startOffset := starts[oldest]
	startLine := totalLines - count + 1
	return startOffset, size, startLine, totalLines, nil
}

func renderNumberedLines(raw []byte, startLine, limit int) ([]byte, int, bool) {
	var output bytes.Buffer
	sourceConsumed := 0
	lineNumber := startLine
	for sourceConsumed < len(raw) {
		remainder := raw[sourceConsumed:]
		lineLength := len(remainder)
		if newline := bytes.IndexByte(remainder, '\n'); newline >= 0 {
			lineLength = newline + 1
		}
		prefix := []byte(strconv.Itoa(lineNumber) + ": ")
		if output.Len()+len(prefix) >= limit {
			available := limit - output.Len()
			if available > 0 {
				_, _ = output.Write(prefix[:available])
			}
			return trimRenderedUTF8(output.Bytes()), sourceConsumed, true
		}
		_, _ = output.Write(prefix)
		available := limit - output.Len()
		if lineLength > available {
			_, _ = output.Write(remainder[:available])
			sourceConsumed += available
			return trimRenderedUTF8(output.Bytes()), sourceConsumed, true
		}
		_, _ = output.Write(remainder[:lineLength])
		sourceConsumed += lineLength
		lineNumber++
	}
	return output.Bytes(), sourceConsumed, false
}

func trimRenderedUTF8(input []byte) []byte {
	if utf8.Valid(input) {
		return input
	}
	trimmed, ok := trimIncompleteUTF8Suffix(input)
	if !ok {
		return nil
	}
	return trimmed
}

func returnedLineEnd(source []byte, startLine int) int {
	if len(source) == 0 || startLine == 0 {
		return 0
	}
	endLine := startLine + bytes.Count(source, []byte{'\n'})
	if source[len(source)-1] == '\n' {
		endLine--
	}
	return endLine
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
