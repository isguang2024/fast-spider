package releaseinfo

import (
	"context"
	"errors"
	"io"
	"testing"
)

type gatedHashReader struct {
	started chan struct{}
	release chan struct{}
	done    bool
}

func (r *gatedHashReader) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	close(r.started)
	<-r.release
	return copy(buffer, []byte("chunk")), nil
}

func TestHashSHA256ContextObservesCancellationDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &gatedHashReader{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, _, err := hashSHA256Context(ctx, reader)
		done <- err
	}()
	<-reader.started
	cancel()
	close(reader.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("hash cancellation error=%v", err)
	}
}
