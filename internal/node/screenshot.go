package node

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
	kbscreenshot "github.com/kbinani/screenshot"
)

const (
	maxDesktopScreenshotPixels int64 = 25_000_000
	maxDesktopScreenshotBytes  int64 = 32 << 20
)

var (
	ErrScreenshotUnavailable = errors.New("screenshot capture unavailable")
	ErrScreenshotTooLarge    = errors.New("screenshot exceeds resource limit")
)

type screenshotCaptureParams struct {
	DisplayIndex int    `json:"displayIndex,omitempty"`
	WindowID     string `json:"windowId,omitempty"`
	Format       string `json:"format,omitempty"`
	Quality      int    `json:"quality,omitempty"`
}

type displaySummary struct {
	Index  int `json:"index"`
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type windowSummary struct {
	WindowID  string    `json:"windowId"`
	Title     string    `json:"title"`
	X         int       `json:"x"`
	Y         int       `json:"y"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *Client) screenshotCapture(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch action {
	case "listDisplays":
		displays, err := activeDisplays()
		if err != nil {
			return nil, err
		}
		items := make([]displaySummary, 0, len(displays))
		for index, bounds := range displays {
			items = append(items, displaySummary{Index: index, X: bounds.Min.X, Y: bounds.Min.Y, Width: bounds.Dx(), Height: bounds.Dy()})
		}
		return map[string]any{"displays": items}, nil
	case "listWindows":
		windows, err := listNativeWindows()
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		items := make([]windowSummary, 0, len(windows))
		for _, window := range windows {
			items = append(items, windowSummary{
				WindowID: makeWindowToken(c.windowTokenKey, window.Handle, windowIdentity(window), now), Title: window.Title,
				X: window.Bounds.Min.X, Y: window.Bounds.Min.Y, Width: window.Bounds.Dx(), Height: window.Bounds.Dy(), ExpiresAt: now.Add(windowTokenTTL),
			})
		}
		return map[string]any{"windows": items}, nil
	case "window":
		var input screenshotCaptureParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid screenshot params: %w", err)
		}
		handle, expectedIdentity, err := parseWindowToken(c.windowTokenKey, input.WindowID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		info, err := nativeWindowInfoForHandle(handle)
		if err != nil {
			return nil, err
		}
		if windowIdentity(info) != expectedIdentity {
			return nil, ErrWindowTokenInvalid
		}
		return c.captureWindowArtifact(ctx, input, info)
	case "desktop", "display":
		var input screenshotCaptureParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid screenshot params: %w", err)
		}
		displays, err := activeDisplays()
		if err != nil {
			return nil, err
		}
		var bounds image.Rectangle
		if action == "display" {
			if input.DisplayIndex < 0 || input.DisplayIndex >= len(displays) {
				return nil, fmt.Errorf("displayIndex is outside the active display range")
			}
			bounds = displays[input.DisplayIndex]
		} else {
			bounds = displays[0]
			for _, display := range displays[1:] {
				bounds = bounds.Union(display)
			}
		}
		return c.captureRectArtifact(ctx, action, input, bounds)
	default:
		return nil, fmt.Errorf("unsupported screenshot action")
	}
}

func (c *Client) captureWindowArtifact(ctx context.Context, input screenshotCaptureParams, window nativeWindowInfo) (map[string]any, error) {
	select {
	case c.screenshotSem <- struct{}{}:
		defer func() { <-c.screenshotSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if pixels := int64(window.Bounds.Dx()) * int64(window.Bounds.Dy()); pixels <= 0 || pixels > maxDesktopScreenshotPixels {
		return nil, ErrScreenshotTooLarge
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" && format != "jpg" {
		return nil, fmt.Errorf("screenshot format must be png or jpeg")
	}
	id, err := security.RandomOpaque("window_")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(c.cfg.DataDir, "screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pngPath := filepath.Join(dir, id+".png")
	if err := captureWindowPNG(window.Handle, pngPath); err != nil {
		_ = os.Remove(pngPath)
		return nil, err
	}
	defer os.Remove(pngPath)
	pngFile, err := os.Open(pngPath)
	if err != nil {
		return nil, err
	}
	config, configErr := png.DecodeConfig(pngFile)
	closeErr := pngFile.Close()
	if configErr != nil {
		return nil, fmt.Errorf("decode captured window metadata: %w", configErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if pixels := int64(config.Width) * int64(config.Height); pixels <= 0 || pixels > maxDesktopScreenshotPixels {
		return nil, ErrScreenshotTooLarge
	}
	info, err := os.Stat(pngPath)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maxDesktopScreenshotBytes {
		return nil, ErrScreenshotTooLarge
	}
	finalPath := pngPath
	contentType := "image/png"
	if format == "jpeg" || format == "jpg" {
		quality := input.Quality
		if quality == 0 {
			quality = 80
		}
		if quality < 20 || quality > 95 {
			return nil, fmt.Errorf("jpeg quality must be between 20 and 95")
		}
		source, err := os.Open(pngPath)
		if err != nil {
			return nil, err
		}
		decoded, decodeErr := png.Decode(source)
		closeErr := source.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		jpegPath := filepath.Join(dir, id+".jpg")
		destination, err := os.OpenFile(jpegPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		encodeErr := jpeg.Encode(destination, decoded, &jpeg.Options{Quality: quality})
		closeErr = destination.Close()
		if encodeErr != nil {
			_ = os.Remove(jpegPath)
			return nil, encodeErr
		}
		if closeErr != nil {
			_ = os.Remove(jpegPath)
			return nil, closeErr
		}
		defer os.Remove(jpegPath)
		finalPath = jpegPath
		contentType = "image/jpeg"
		info, err = os.Stat(finalPath)
		if err != nil {
			return nil, err
		}
		if info.Size() <= 0 || info.Size() > maxDesktopScreenshotBytes {
			return nil, ErrScreenshotTooLarge
		}
	}
	published, err := c.publishScreenshotPresentation(ctx, finalPath, filepath.Base(finalPath), contentType)
	if err != nil {
		return nil, err
	}
	c.cfg.Logger.Info("window screenshot captured", "windowId", input.WindowID, "width", config.Width, "height", config.Height)
	published["target"] = "window"
	published["windowId"] = input.WindowID
	published["title"] = window.Title
	published["x"] = window.Bounds.Min.X
	published["y"] = window.Bounds.Min.Y
	published["width"] = config.Width
	published["height"] = config.Height
	return published, nil
}

func activeDisplays() ([]image.Rectangle, error) {
	count := kbscreenshot.NumActiveDisplays()
	if count <= 0 || count > 32 {
		return nil, ErrScreenshotUnavailable
	}
	out := make([]image.Rectangle, 0, count)
	for index := 0; index < count; index++ {
		bounds := kbscreenshot.GetDisplayBounds(index)
		if bounds.Empty() || bounds.Dx() > 16384 || bounds.Dy() > 16384 {
			return nil, ErrScreenshotUnavailable
		}
		out = append(out, bounds)
	}
	return out, nil
}

func (c *Client) captureRectArtifact(ctx context.Context, target string, input screenshotCaptureParams, bounds image.Rectangle) (map[string]any, error) {
	select {
	case c.screenshotSem <- struct{}{}:
		defer func() { <-c.screenshotSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	pixels := int64(bounds.Dx()) * int64(bounds.Dy())
	if pixels <= 0 || pixels > maxDesktopScreenshotPixels {
		return nil, ErrScreenshotTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	captured, err := kbscreenshot.CaptureRect(bounds)
	if err != nil {
		return nil, fmt.Errorf("capture screenshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" && format != "jpg" {
		return nil, fmt.Errorf("screenshot format must be png or jpeg")
	}
	ext := "png"
	contentType := "image/png"
	if format == "jpeg" || format == "jpg" {
		ext = "jpg"
		contentType = "image/jpeg"
	}
	id, err := security.RandomOpaque("shot_")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(c.cfg.DataDir, "screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+"."+ext)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var encodeErr error
	if contentType == "image/png" {
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		encodeErr = encoder.Encode(file, captured)
	} else {
		quality := input.Quality
		if quality == 0 {
			quality = 80
		}
		if quality < 20 || quality > 95 {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("jpeg quality must be between 20 and 95")
		}
		encodeErr = jpeg.Encode(file, captured, &jpeg.Options{Quality: quality})
	}
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(path)
		return nil, encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maxDesktopScreenshotBytes {
		return nil, ErrScreenshotTooLarge
	}
	published, err := c.publishScreenshotPresentation(ctx, path, filepath.Base(path), contentType)
	if err != nil {
		return nil, err
	}
	c.cfg.Logger.Info("desktop screenshot captured", "target", target, "width", bounds.Dx(), "height", bounds.Dy())
	published["target"] = target
	published["x"] = bounds.Min.X
	published["y"] = bounds.Min.Y
	published["width"] = bounds.Dx()
	published["height"] = bounds.Dy()
	return published, nil
}
