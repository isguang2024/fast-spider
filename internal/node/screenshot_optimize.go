package node

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	screenshotOptimizeThresholdBytes int64 = 1 << 20
	screenshotOptimizeMaxWidth             = 2560
	screenshotOptimizeMaxPixels      int64 = 4_000_000
	screenshotOptimizeJPEGQuality          = 82
)

type preparedScreenshotPresentation struct {
	Path            string
	FileName        string
	ContentType     string
	SourceSizeBytes int64
	SourceWidth     int
	SourceHeight    int
	Width           int
	Height          int
	Optimized       bool
	cleanupPath     string
}

func (p preparedScreenshotPresentation) cleanup() {
	if p.cleanupPath != "" {
		_ = os.Remove(p.cleanupPath)
	}
}

func prepareScreenshotPresentation(filePath, logicalName, contentType, tempDir string) (preparedScreenshotPresentation, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return preparedScreenshotPresentation{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return preparedScreenshotPresentation{}, ErrNotRegularFile
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		return preparedScreenshotPresentation{}, fmt.Errorf("screenshot content type must be image/png or image/jpeg")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return preparedScreenshotPresentation{}, err
	}
	config, _, decodeErr := image.DecodeConfig(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return preparedScreenshotPresentation{}, fmt.Errorf("decode screenshot metadata: %w", decodeErr)
	}
	if closeErr != nil {
		return preparedScreenshotPresentation{}, closeErr
	}
	if config.Width <= 0 || config.Height <= 0 {
		return preparedScreenshotPresentation{}, ErrScreenshotTooLarge
	}

	base := preparedScreenshotPresentation{
		Path: filePath, FileName: logicalName, ContentType: mimeType,
		SourceSizeBytes: info.Size(), SourceWidth: config.Width, SourceHeight: config.Height,
		Width: config.Width, Height: config.Height,
	}
	pixels := int64(config.Width) * int64(config.Height)
	resizeRequired := config.Width > screenshotOptimizeMaxWidth || pixels > screenshotOptimizeMaxPixels
	compressionRequired := info.Size() > screenshotOptimizeThresholdBytes
	if !resizeRequired && !compressionRequired {
		return base, nil
	}

	scale := 1.0
	if config.Width > screenshotOptimizeMaxWidth {
		scale = math.Min(scale, float64(screenshotOptimizeMaxWidth)/float64(config.Width))
	}
	if pixels > screenshotOptimizeMaxPixels {
		scale = math.Min(scale, math.Sqrt(float64(screenshotOptimizeMaxPixels)/float64(pixels)))
	}
	targetWidth := config.Width
	targetHeight := config.Height
	if scale < 1 {
		targetWidth = max(1, int(math.Floor(float64(config.Width)*scale)))
		targetHeight = max(1, int(math.Floor(float64(config.Height)*scale)))
	}

	source, err := os.Open(filePath)
	if err != nil {
		return preparedScreenshotPresentation{}, err
	}
	decoded, _, decodeErr := image.Decode(source)
	closeErr = source.Close()
	if decodeErr != nil {
		return preparedScreenshotPresentation{}, fmt.Errorf("decode screenshot: %w", decodeErr)
	}
	if closeErr != nil {
		return preparedScreenshotPresentation{}, closeErr
	}
	var output image.Image = decoded
	if targetWidth != config.Width || targetHeight != config.Height {
		output = resizeImageBilinear(decoded, targetWidth, targetHeight)
	}
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return preparedScreenshotPresentation{}, err
	}
	destination, err := os.CreateTemp(tempDir, "presentation-*.jpg")
	if err != nil {
		return preparedScreenshotPresentation{}, err
	}
	jpegPath := destination.Name()
	encodeErr := jpeg.Encode(destination, output, &jpeg.Options{Quality: screenshotOptimizeJPEGQuality})
	closeErr = destination.Close()
	if encodeErr != nil || closeErr != nil {
		_ = os.Remove(jpegPath)
		if encodeErr != nil {
			return preparedScreenshotPresentation{}, encodeErr
		}
		return preparedScreenshotPresentation{}, closeErr
	}
	jpegInfo, err := os.Stat(jpegPath)
	if err != nil {
		_ = os.Remove(jpegPath)
		return preparedScreenshotPresentation{}, err
	}
	if jpegInfo.Size() <= 0 || jpegInfo.Size() > maxDesktopScreenshotBytes {
		_ = os.Remove(jpegPath)
		return preparedScreenshotPresentation{}, ErrScreenshotTooLarge
	}
	if !resizeRequired && jpegInfo.Size() >= info.Size() {
		_ = os.Remove(jpegPath)
		return base, nil
	}

	name := strings.TrimSuffix(logicalName, filepath.Ext(logicalName)) + ".jpg"
	if strings.TrimSpace(strings.TrimSuffix(logicalName, filepath.Ext(logicalName))) == "" {
		name = "screenshot.jpg"
	}
	return preparedScreenshotPresentation{
		Path: jpegPath, FileName: name, ContentType: "image/jpeg",
		SourceSizeBytes: info.Size(), SourceWidth: config.Width, SourceHeight: config.Height,
		Width: targetWidth, Height: targetHeight, Optimized: true, cleanupPath: jpegPath,
	}, nil
}

func resizeImageBilinear(source image.Image, width, height int) *image.NRGBA {
	bounds := source.Bounds()
	destination := image.NewNRGBA(image.Rect(0, 0, width, height))
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	if sourceWidth == width && sourceHeight == height && bounds.Min.X == 0 && bounds.Min.Y == 0 {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				destination.SetNRGBA(x, y, color.NRGBAModel.Convert(source.At(x, y)).(color.NRGBA))
			}
		}
		return destination
	}

	scaleX := float64(sourceWidth) / float64(width)
	scaleY := float64(sourceHeight) / float64(height)
	minX, minY := bounds.Min.X, bounds.Min.Y
	maxX, maxY := bounds.Max.X-1, bounds.Max.Y-1
	for y := 0; y < height; y++ {
		sy := (float64(y)+0.5)*scaleY - 0.5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		y0 += minY
		if y0 < minY {
			y0 = minY
			fy = 0
		}
		y1 := min(y0+1, maxY)
		for x := 0; x < width; x++ {
			sx := (float64(x)+0.5)*scaleX - 0.5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			x0 += minX
			if x0 < minX {
				x0 = minX
				fx = 0
			}
			x1 := min(x0+1, maxX)
			c00 := color.NRGBAModel.Convert(source.At(x0, y0)).(color.NRGBA)
			c10 := color.NRGBAModel.Convert(source.At(x1, y0)).(color.NRGBA)
			c01 := color.NRGBAModel.Convert(source.At(x0, y1)).(color.NRGBA)
			c11 := color.NRGBAModel.Convert(source.At(x1, y1)).(color.NRGBA)
			destination.SetNRGBA(x, y, color.NRGBA{
				R: bilinearChannel(c00.R, c10.R, c01.R, c11.R, fx, fy),
				G: bilinearChannel(c00.G, c10.G, c01.G, c11.G, fx, fy),
				B: bilinearChannel(c00.B, c10.B, c01.B, c11.B, fx, fy),
				A: bilinearChannel(c00.A, c10.A, c01.A, c11.A, fx, fy),
			})
		}
	}
	return destination
}

func bilinearChannel(c00, c10, c01, c11 uint8, fx, fy float64) uint8 {
	top := float64(c00)*(1-fx) + float64(c10)*fx
	bottom := float64(c01)*(1-fx) + float64(c11)*fx
	value := top*(1-fy) + bottom*fy
	if value <= 0 {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return uint8(math.Round(value))
}
