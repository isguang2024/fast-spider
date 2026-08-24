# Free Local Deployment

[简体中文](free-local-deployment.zh-CN.md)

This guide describes how to run Fast Spider without renting a server.

The intended setup is:

- Hub runs on a local computer.
- Node runs on the same local computer or another machine in the same LAN.
- A free or free-tier tunnel exposes only the Hub HTTP endpoint when cloud access is needed.

This is useful for personal use, demos, testing and small private workflows. It is not a substitute for a hardened production server.

## Modes

| Mode | Cost | Public Internet required | Best for |
|---|---:|---:|---|
| Local only | 0 | No | Web console, local testing, one-machine use |
| Stable tunnel | 0 or free-tier | Yes | ChatGPT/MCP/OAuth callbacks and repeatable demos |
| Temporary tunnel | 0 or free-tier | Yes | Short demos and webhook-style testing |

## Fast path: `share`

For a disposable first run, let the CLI create the local Hub profile and
owner account:

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```

The command keeps Hub on loopback, binds the Node to the selected project and
prints a 30-day Node Connection Token plus the Node command. It does not start
Node automatically. Run the printed command in a second terminal, then use
the printed MCP URL. MCP authentication is OAuth; the printed Bearer token is
only for registering the Node.

Use a free tunnel when a cloud MCP client must reach the local Hub:

```bash
go run ./cmd/spiderctl share --project . --tunnel cloudflare
go run ./cmd/spiderctl share --project . --tunnel ngrok
```

`cloudflared` and `ngrok` must already be installed and available in `PATH`.
The CLI checks this before starting the Hub and gives an install hint when the
dependency is missing. Cloudflare Quick Tunnel URLs and default ngrok URLs are
temporary; do not treat them as a permanent service address.

If the CLI is installed outside the source checkout, provide a
`fast-spider-hub` executable or set `FAST_SPIDER_SOURCE_ROOT` to the source
tree. A supplied `--data-dir` must stay outside the project root so the
Project-mode policy cannot read Hub secrets as project files.

## Security boundary

A tunnel exposes the local Hub to the Internet. Keep the boundary narrow:

- Bind Hub to loopback: `127.0.0.1:8787`.
- Tunnel only `http://127.0.0.1:8787`.
- Do not expose Node loopback UI.
- Use a unique strong `FAST_SPIDER_ADMIN_PASSWORD`.
- Keep `--allowed-hosts` limited to localhost plus the exact tunnel hostname.
- Rotate connection tokens if a tunnel URL or demo is shared publicly.
- Treat LocalTunnel/random tunnel URLs as temporary and untrusted.

Node still executes with the operating system permissions of the user running Node. A free tunnel changes network reachability, not the local privilege model.

Project mode narrows explicit file, search, working-directory, Git, artifact
and AI input paths, and disables native desktop/window screenshots. It does not
sandbox arbitrary shell interpreters, Git remotes, browser network access or
provider credentials. Use Machine mode only for private, advanced workflows.

## Option A: local only

Use this when all clients are local and no cloud service needs to call Hub.

```bash
export FAST_SPIDER_ADMIN_PASSWORD='<replace-with-a-strong-password>'

go run ./cmd/hub \
  --listen 127.0.0.1:8787 \
  --data-dir ./data \
  --allowed-hosts localhost,127.0.0.1 \
  --public-base-url http://127.0.0.1:8787
```

Create the first owner:

```bash
go run ./cmd/spiderctl setup-url \
  --public-url http://127.0.0.1:8787 \
  --allow-insecure \
  --bootstrap-token-file ./data/bootstrap-token
```

Start the local Node UI:

```bash
go run ./cmd/node ui
```

In the Node UI connection page, use:

```text
Hub URL: http://127.0.0.1:8787
```

This mode does not require a domain, TLS, a VPS, or router port forwarding.

## Option B: Cloudflare Tunnel

Use this when you need a stable public HTTPS hostname and you have a domain managed by Cloudflare.

Cloudflare Tunnel maps a public hostname to a local service. Configure the public hostname to route to:

```text
http://127.0.0.1:8787
```

Then start Hub with the public hostname:

```bash
export FAST_SPIDER_ADMIN_PASSWORD='<replace-with-a-strong-password>'

PUBLIC_HOST='fast-spider.example.com'
PUBLIC_URL="https://${PUBLIC_HOST}"

go run ./cmd/hub \
  --listen 127.0.0.1:8787 \
  --data-dir ./data \
  --allowed-hosts localhost,127.0.0.1,${PUBLIC_HOST} \
  --public-base-url ${PUBLIC_URL} \
  --oauth-redirect-hosts chatgpt.com,localhost,127.0.0.1,::1
```

Create the first owner using the same public URL:

```bash
go run ./cmd/spiderctl setup-url \
  --public-url ${PUBLIC_URL} \
  --bootstrap-token-file ./data/bootstrap-token
```

Connect Node either through the public URL or local URL. Use the public URL when you want to test the same path used by cloud clients:

```text
Hub URL: https://fast-spider.example.com
```

## Option C: ngrok free-tier tunnel

Use this for quick setup without your own server. ngrok free-tier hostnames and limits may change, so check the current ngrok plan limits before relying on it for long-running use.

Start the tunnel:

```bash
ngrok http 8787
```

If your account has an assigned static development domain, prefer it:

```bash
ngrok http --domain=<your-ngrok-dev-domain> 8787
```

Start Hub with the hostname shown by ngrok:

```bash
export FAST_SPIDER_ADMIN_PASSWORD='<replace-with-a-strong-password>'

PUBLIC_HOST='<your-ngrok-hostname>'
PUBLIC_URL="https://${PUBLIC_HOST}"

go run ./cmd/hub \
  --listen 127.0.0.1:8787 \
  --data-dir ./data \
  --allowed-hosts localhost,127.0.0.1,${PUBLIC_HOST} \
  --public-base-url ${PUBLIC_URL} \
  --oauth-redirect-hosts chatgpt.com,localhost,127.0.0.1,::1
```

If the ngrok hostname changes, restart Hub with the new `--public-base-url` and update any external client configuration.

## Option D: LocalTunnel or similar temporary tunnel

Use this only for short demos. LocalTunnel-style services can assign a random hostname and may be less stable for long-lived OAuth or MCP workflows.

```bash
npx localtunnel --port 8787
```

Then start Hub with the returned hostname:

```bash
export FAST_SPIDER_ADMIN_PASSWORD='<replace-with-a-strong-password>'

PUBLIC_HOST='<returned-subdomain>.loca.lt'
PUBLIC_URL="https://${PUBLIC_HOST}"

go run ./cmd/hub \
  --listen 127.0.0.1:8787 \
  --data-dir ./data \
  --allowed-hosts localhost,127.0.0.1,${PUBLIC_HOST} \
  --public-base-url ${PUBLIC_URL} \
  --oauth-redirect-hosts chatgpt.com,localhost,127.0.0.1,::1
```

Do not use a random temporary tunnel URL as a permanent MCP or OAuth endpoint.

## Windows users

PowerShell users can run the same source command from the repository root:

```text
go run .\cmd\spiderctl share --project . --tunnel none
```

Keep the Hub terminal open. Copy the printed Node command into a second
PowerShell window. If `cloudflared` or `ngrok` is installed but not found,
restart the shell after adding its installation directory to `PATH`.

## Node connection examples

Same machine:

```text
Hub URL: http://127.0.0.1:8787
```

Same LAN:

```text
Hub URL: http://<hub-lan-ip>:8787
```

Public tunnel:

```text
Hub URL: https://<public-tunnel-hostname>
```

For LAN usage, bind Hub to a LAN interface only if you trust the LAN. Do not bind Hub to `0.0.0.0` unless you also understand firewall exposure.

## Recommended personal setup

For personal use without a server:

1. Run Hub on your main desktop or a mini PC.
2. Keep Hub on loopback.
3. Use Cloudflare Tunnel when you need a stable public hostname.
4. Use ngrok or LocalTunnel only for short-lived tests.
5. Run Node on the same computer first.
6. Add more Nodes only after the first local path works.

## Troubleshooting

### Browser shows 400 or host rejected

Add the exact tunnel hostname to `--allowed-hosts` and restart Hub.

### OAuth or MCP discovery uses the wrong URL

Set `--public-base-url` to the external HTTPS URL that cloud clients can reach.

### Node connects locally but cloud clients fail

The tunnel is not routing to `http://127.0.0.1:8787`, or Hub was started with a local-only `--public-base-url`.

### Tunnel hostname changed

Restart Hub with the new hostname and update external client settings. Prefer a stable tunnel hostname for repeated use.

### `share` says that the Hub cannot be started

Install `fast-spider-hub`, set `FAST_SPIDER_HUB_BIN`, or run the command from
the Fast Spider source tree. An installed `spiderctl` by itself is not a
complete Hub distribution.

### `cloudflared` or `ngrok` is not in PATH

Install the selected tunnel client, make sure the executable is visible to the
same shell, and rerun `share`. The command intentionally does not download or
install tunnel software.

### Project path is rejected

The printed Node command must include `--project-root` pointing to the same
directory used by `share`. Do not replace it with a sibling directory or a
Machine-mode Node when testing the public onboarding path.

### ChatGPT cannot connect to the MCP URL

Use `--tunnel cloudflare` or `--tunnel ngrok`, confirm that the public hostname
appears in Hub `--allowed-hosts`, and ensure Hub was started with the same
public URL in `--public-base-url`. The Node Connection Token is not an MCP
OAuth credential; complete OAuth at the MCP URL.
