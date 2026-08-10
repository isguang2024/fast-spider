package node

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareScreenshotPresentationResizesLargePNG(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "large.png")
	img := image.NewNRGBA(image.Rect(0, 0, 3000, 1000))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 251), G: uint8(y % 239), B: uint8((x + y) % 241), A: 255})
		}
	}
	writePNGForOptimizeTest(t, sourcePath, img)

	prepared, err := prepareScreenshotPresentation(sourcePath, "large.png", "image/png", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	if !prepared.Optimized || prepared.ContentType != "image/jpeg" || prepared.FileName != "large.jpg" {
		t.Fatalf("prepared=%+v", prepared)
	}
	if prepared.SourceWidth != 3000 || prepared.SourceHeight != 1000 || prepared.Width != screenshotOptimizeMaxWidth {
		t.Fatalf("dimensions source=%dx%d presentation=%dx%d", prepared.SourceWidth, prepared.SourceHeight, prepared.Width, prepared.Height)
	}
	if int64(prepared.Width)*int64(prepared.Height) > screenshotOptimizeMaxPixels {
		t.Fatalf("optimized pixels=%d exceeds=%d", int64(prepared.Width)*int64(prepared.Height), screenshotOptimizeMaxPixels)
	}
	file, err := os.Open(prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := jpeg.DecodeConfig(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("decode jpeg config err=%v close=%v", err, closeErr)
	}
	if config.Width != prepared.Width || config.Height != prepared.Height {
		t.Fatalf("jpeg dimensions=%dx%d prepared=%dx%d", config.Width, config.Height, prepared.Width, prepared.Height)
	}
	optimizedPath := prepared.Path
	prepared.cleanup()
	if _, err := os.Stat(optimizedPath); !os.IsNotExist(err) {
		t.Fatalf("optimized temp file still exists: %v", err)
	}
}

func TestPrepareScreenshotPresentationKeepsSmallPNG(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "small.png")
	img := image.NewNRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 245, G: 245, B: 245, A: 255})
		}
	}
	writePNGForOptimizeTest(t, sourcePath, img)
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > screenshotOptimizeThresholdBytes {
		t.Fatalf("small test PNG unexpectedly large: %d", info.Size())
	}

	prepared, err := prepareScreenshotPresentation(sourcePath, "small.png", "image/png", dir)
	if err != nil {
		t.Fatal(err)
	}
	prepared.cleanup()
	if prepared.Optimized || prepared.Path != sourcePath || prepared.ContentType != "image/png" || prepared.Width != 640 || prepared.Height != 480 {
		t.Fatalf("prepared=%+v", prepared)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("source screenshot was removed: %v", err)
	}
}

func writePNGForOptimizeTest(t *testing.T, path string, img image.Image) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	encodeErr := encoder.Encode(file, img)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatalf("encode png err=%v close=%v", encodeErr, closeErr)
	}
}
