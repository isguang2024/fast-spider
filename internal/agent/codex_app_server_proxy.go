package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// codexAppServerSocketPath returns the opt-in control socket used to connect
// to an externally owned Codex app-server. An empty value keeps the existing
// Node-owned stdio process behavior.
func codexAppServerSocketPath() (string, error) {
	socketPath := strings.TrimSpace(os.Getenv(codexAppServerSocketEnv))
	if socketPath == "" {
		return "", nil
	}
	if !filepath.IsAbs(socketPath) {
		return "", fmt.Errorf("%s must be an absolute app-server socket path", codexAppServerSocketEnv)
	}
	return filepath.Clean(socketPath), nil
}

func codexAppServerCommandArgs(socketPath string) []string {
	if strings.TrimSpace(socketPath) != "" {
		return []string{"app-server", "proxy", "--sock", socketPath}
	}
	return []string{"app-server", "--stdio"}
}

func (a *CodexAdapter) executionMetadata() (string, string) {
	if a.usesExternalAppServer() {
		return "external_app_server", "external_app_server"
	}
	return "bridge_owned", "node_agent_bridge"
}

func (a *CodexAdapter) usesExternalAppServer() bool {
	socketPath, err := codexAppServerSocketPath()
	return err == nil && socketPath != ""
}

// dialCodexAppServerProxy turns the proxy process's stdio into the WebSocket
// connection expected by the unix:// app-server transport. The proxy forwards
// bytes, so sending JSONL directly would make the server interpret the first
// byte as an invalid WebSocket frame.
func dialCodexAppServerProxy(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) (*websocket.Conn, error) {
	pipe := &codexProxyPipeConn{reader: stdout, writer: stdin}
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return pipe, nil
		},
	}
	client := &http.Client{Transport: transport}
	conn, _, err := websocket.Dial(ctx, "ws://codex-app-server.local/rpc", &websocket.DialOptions{
		HTTPClient:      client,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		_ = pipe.Close()
		transport.CloseIdleConnections()
		return nil, err
	}
	return conn, nil
}

// codexProxyPipeConn is the small net.Conn adapter needed by net/http to send
// the WebSocket upgrade request through codex app-server proxy's stdin/stdout.
// It deliberately does not own or terminate the external app-server; closing
// it only closes this proxy connection's pipes.
type codexProxyPipeConn struct {
	reader io.ReadCloser
	writer io.WriteCloser

	closeOnce sync.Once
	closeErr  error
}

func (c *codexProxyPipeConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *codexProxyPipeConn) Write(p []byte) (int, error) { return c.writer.Write(p) }

func (c *codexProxyPipeConn) Close() error {
	c.closeOnce.Do(func() {
		if err := c.writer.Close(); err != nil {
			c.closeErr = err
		}
		if err := c.reader.Close(); err != nil && c.closeErr == nil {
			c.closeErr = err
		}
	})
	return c.closeErr
}

func (c *codexProxyPipeConn) LocalAddr() net.Addr              { return codexProxyAddr("local") }
func (c *codexProxyPipeConn) RemoteAddr() net.Addr             { return codexProxyAddr("codex-app-server") }
func (c *codexProxyPipeConn) SetDeadline(time.Time) error      { return nil }
func (c *codexProxyPipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *codexProxyPipeConn) SetWriteDeadline(time.Time) error { return nil }

type codexProxyAddr string

func (a codexProxyAddr) Network() string { return "codex-app-server-proxy" }
func (a codexProxyAddr) String() string  { return string(a) }
