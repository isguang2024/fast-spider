package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

func TestReleaseManifestCacheMergesInvalidatesAndBounds(t *testing.T) {
	cache := releaseManifestCacheStore{max: 2}
	stamp := releaseManifestStamp{artifactSize: 10, artifactModNano: 1, versionSize: 6, versionModNano: 1}
	var loads atomic.Int32
	loader := func(context.Context) (releaseinfo.Manifest, error) {
		loads.Add(1)
		time.Sleep(25 * time.Millisecond)
		return releaseinfo.Manifest{Version: "0.1.0", SHA256: "first"}, nil
	}

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			manifest, err := cache.get(context.Background(), "release-a", stamp, loader)
			if err == nil && manifest.SHA256 != "first" {
				t.Errorf("cached manifest=%+v", manifest)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("concurrent cache loads=%d, want 1", got)
	}

	changedStamp := stamp
	changedStamp.artifactModNano++
	changed, err := cache.get(context.Background(), "release-a", changedStamp, func(context.Context) (releaseinfo.Manifest, error) {
		loads.Add(1)
		return releaseinfo.Manifest{Version: "0.1.1", SHA256: "second"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.SHA256 != "second" || loads.Load() != 2 {
		t.Fatalf("changed stamp did not invalidate cache: manifest=%+v loads=%d", changed, loads.Load())
	}
	for _, key := range []string{"release-b", "release-c"} {
		if _, err := cache.get(context.Background(), key, stamp, func(context.Context) (releaseinfo.Manifest, error) {
			return releaseinfo.Manifest{SHA256: key}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 2 {
		t.Fatalf("bounded cache entries=%d, want 2", entries)
	}
}

func TestReleaseManifestCacheInvalidatesAtomicSameMetadataReplacement(t *testing.T) {
	root := t.TempDir()
	artifactPath := filepath.Join(root, "artifact.bin")
	versionPath := filepath.Join(root, "version.txt")
	if err := os.WriteFile(artifactPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionPath, []byte("0.1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, firstStamp, err := releaseManifestInputs(artifactPath, versionPath)
	if err != nil {
		t.Fatal(err)
	}
	cache := releaseManifestCacheStore{max: 2}
	var loads atomic.Int32
	if _, err := cache.get(context.Background(), "release", firstStamp, func(context.Context) (releaseinfo.Manifest, error) {
		loads.Add(1)
		return releaseinfo.Manifest{SHA256: "first"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	tmpPath := filepath.Join(root, "artifact.next")
	if err := os.WriteFile(tmpPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmpPath, firstInfo.ModTime(), firstInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, artifactPath); err != nil {
		t.Fatal(err)
	}
	_, secondStamp, err := releaseManifestInputs(artifactPath, versionPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstStamp == secondStamp {
		t.Fatal("atomic artifact replacement with preserved size and mtime reused the old stamp")
	}
	manifest, err := cache.get(context.Background(), "release", secondStamp, func(context.Context) (releaseinfo.Manifest, error) {
		loads.Add(1)
		return releaseinfo.Manifest{SHA256: "other"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SHA256 != "other" || loads.Load() != 2 {
		t.Fatalf("same-metadata replacement did not invalidate cache: manifest=%+v loads=%d", manifest, loads.Load())
	}
}

func TestReleaseManifestCacheCanceledWaiterReturnsWithoutStoppingActiveCaller(t *testing.T) {
	cache := releaseManifestCacheStore{max: 2}
	stamp := releaseManifestStamp{artifactSize: 10, artifactModNano: 1, versionSize: 6, versionModNano: 1}
	loadStarted := make(chan struct{})
	allowLoad := make(chan struct{})
	loader := func(ctx context.Context) (releaseinfo.Manifest, error) {
		close(loadStarted)
		select {
		case <-allowLoad:
			return releaseinfo.Manifest{SHA256: "loaded"}, nil
		case <-ctx.Done():
			return releaseinfo.Manifest{}, ctx.Err()
		}
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := cache.get(context.Background(), "release", stamp, loader)
		firstDone <- err
	}()
	<-loadStarted

	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() {
		_, err := cache.get(waitCtx, "release", stamp, loader)
		waitDone <- err
	}()
	cancelWait()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled singleflight waiter did not return")
	}
	close(allowLoad)
	if err := <-firstDone; err != nil {
		t.Fatalf("active cache caller was canceled with an unrelated waiter: %v", err)
	}
}

func TestReleaseManifestCacheCancelsLoadWhenAllWaitersLeave(t *testing.T) {
	cache := releaseManifestCacheStore{max: 1}
	stamp := releaseManifestStamp{artifactSize: 10, artifactModNano: 1, versionSize: 6, versionModNano: 1}
	loadCanceled := make(chan struct{})
	loader := func(ctx context.Context) (releaseinfo.Manifest, error) {
		<-ctx.Done()
		close(loadCanceled)
		return releaseinfo.Manifest{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := cache.get(ctx, "release", stamp, loader)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cache caller error=%v", err)
	}
	select {
	case <-loadCanceled:
	case <-time.After(time.Second):
		t.Fatal("singleflight load was left running after all waiters canceled")
	}
}
