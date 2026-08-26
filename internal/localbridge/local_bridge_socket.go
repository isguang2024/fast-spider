package localbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func Endpoint(dataDir string) string {
	return platformLocalBridgeEndpoint(dataDir)
}

func runLocalBridgeServer(ctx context.Context, dataDir string, handler func(context.Context, io.ReadWriteCloser)) error {
	endpoint := Endpoint(dataDir)
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return err
	}
	if err := prepareLocalBridgeSocket(endpoint); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen local bridge: %w", err)
	}
	if err := hardenLocalBridgeSocket(endpoint); err != nil {
		listener.Close()
		_ = os.Remove(endpoint)
		return err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	var connections sync.WaitGroup
	defer func() {
		cancel()
		_ = listener.Close()
		connections.Wait()
		_ = os.Remove(endpoint)
	}()
	go func() {
		<-serverCtx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if serverCtx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept local bridge: %w", err)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			handler(serverCtx, conn)
		}()
	}
}

func prepareLocalBridgeSocket(endpoint string) error {
	info, err := os.Lstat(endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("local bridge path exists and is not a socket")
	}
	conn, dialErr := net.DialTimeout("unix", endpoint, 150*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return fmt.Errorf("%w: local bridge is already running", ErrUnavailable)
	}
	return os.Remove(endpoint)
}

func dialLocalBridge(ctx context.Context, dataDir string) (io.ReadWriteCloser, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", Endpoint(dataDir))
	if err != nil {
		return nil, fmt.Errorf("connect local bridge: %w", err)
	}
	return conn, nil
}
