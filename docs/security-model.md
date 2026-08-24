# Fast Spider Security Model

This document explains the user-facing trust boundaries. It is not a promise
of OS-level sandboxing and it does not replace [SECURITY.md](../SECURITY.md),
which describes vulnerability reporting.

## The short version

Fast Spider is a local automation agent reached through a Hub. The Hub routes
authenticated requests; the Node performs work with the operating-system
permissions of the account that started it.

The recommended public onboarding profile is **Project mode**:

```bash
go run ./cmd/spiderctl share --project . --tunnel none
```

Project mode is a Node-side path-constraint profile. It is not a container,
job object, restricted Windows token, network firewall or secret vault.

## What each surface can do

### ChatGPT and other MCP clients

An MCP client can discover and call the tools exposed by the Hub after OAuth
authorization. The effective result depends on the owner, the selected Node,
the capability and the Node profile. An MCP client cannot use a Node
Connection Token as an MCP login.

MCP requests can result in local file operations, jobs, Git actions, browser
actions, artifacts or AI-provider calls when the corresponding capability is
available. Treat an authorized MCP client as a powerful automation caller.

### Direct API clients

Direct API requests use a separate, short-lived Direct Access Key. Keys can be
read-only or receive explicit scopes such as file writes, shell, Git, browser,
AI, context writes or artifact writes. A Direct Key is not an OAuth token and a
Connection Token cannot be used at `/direct/v1`.

### Hub

The Hub stores account, machine, token, OAuth, audit and routing metadata. It
routes capability parameters and results between authorized clients and
online Nodes. It can therefore observe request metadata and the data returned
by a capability. Do not run a Hub on a host you do not trust.

The Hub does not mount a Node file system. Local file access happens on the
Node, under the Node process account.

### Node

The Node owns the local private key and runs file, shell, build, Git, browser,
screenshot, artifact and AI operations. The operating-system account remains
the fundamental machine trust boundary. A Node can often reach local files,
processes, credentials and networks that this account can reach.

## Project mode versus Machine mode

| Profile | Default use | What is bounded | What is not promised |
|---|---|---|---|
| Project mode | Public onboarding and demos | Explicit file/search paths, shell/build cwd, Git repository/worktree paths, working context, artifact paths and explicit AI input paths | OS-level isolation, arbitrary shell behavior, browser network access, Git credentials, provider credentials or child-process access |
| Machine mode | Private advanced use | The existing OS account, capability and Hub authorization controls | A project directory boundary |

In Project mode, the Node resolves the configured project root and checks the
canonical path before dispatching the capability. Existing paths must resolve
inside the root; new file targets must have an existing in-root parent. Git
worktree creation requires an explicit in-project target. Native desktop,
display and window screenshots are disabled because a project path cannot
protect other windows on the user's screen.

The path policy is enforced before the capability dispatcher, so MCP, Direct
API, Local Bridge and other Node requests share the same check. This still
does not remove time-of-check/time-of-use or OS reparse-point risk from a
general-purpose user process; use a lower-privilege OS account or a real
sandbox when those threats matter.

## Capability risks

### Shell and Build

Project mode limits the explicit working directory. It does not make a shell
command safe: an argv entry can invoke an interpreter, a script, a compiler,
a package manager or a child process that accesses another path or the
network. Build commands can modify the project and may use credentials in the
user environment. Keep write-capable keys private and review commands before
approving them.

### Git

Git actions can modify history, create commits and, for explicitly permitted
operations, contact remotes. Hooks, filters, credential helpers and remote
configuration are part of the local Git trust boundary. Project mode keeps
the repository and explicit worktree paths inside the project, but it does not
turn a remote push into a read-only operation.

### Browser

Browser automation can visit public sites and any HTTP(S) service reachable by
the Node, including local or private network services. Do not treat a browser
page as a harmless viewer. Never put credentials in navigation URLs. Review
downloads, login state, redirects and pages that can trigger side effects.

### Screenshot

Machine mode can capture desktop, display and window images when the graphical
runtime permits it. These images may contain chat, passwords, source code or
personal information. Project mode denies native desktop/display/window
capture. Browser page screenshots remain subject to browser and artifact
risks.

### AI Control

AI Control can create or continue local Codex, Claude Code or other provider
sessions. A provider may read, edit, execute or send project material according
to its own harness and credentials. Project mode checks the explicit working
directory, skills, mentions and local images, but it is not proof that an
upstream provider or child process is OS-sandboxed. Use provider-specific
approval, data-sharing and account settings as an additional boundary.

## Credential types and revocation

- **Bootstrap token**: one-time first-owner setup secret, carried only in a
  setup URL fragment and consumed by the Hub. Never put it in an issue, prompt,
  screenshot or log.
- **Connection Token**: registers a Node. `share` prints it once for the
  printed Node command. It is not an MCP or Direct API credential. Revoke it
  in Web Console after registration or if it was exposed.
- **Device Token**: short-lived Node runtime credential. Do not copy or share
  it.
- **OAuth grant/token**: authenticates an MCP client. Revoke the OAuth client
  or authorization in Web Console.
- **Direct Access Key**: authenticates `/direct/v1` with its own scopes,
  expiry, optional machine binding and rate limit. Revoke it in Web Console.
- **Node and Hub private keys**: local identity material. Keep data
  directories private and back them up as sensitive data.

If a credential is exposed, stop sharing the URL or log, revoke the relevant
token/key/device or OAuth authorization, and create a replacement. Changing a
Connection Token does not automatically revoke an already registered Node;
revoke the machine separately.

## Free tunnels

A tunnel changes network reachability, not the Node's local privileges. Keep
Hub bound to loopback and tunnel only the Hub address. Add only the exact
public hostname to `--allowed-hosts`, set the same external URL as
`--public-base-url`, and use HTTPS for non-loopback public addresses.

Cloudflare Quick Tunnel and default ngrok URLs are temporary and can change.
Anyone who can reach the public Hub still needs valid authentication, but a
public endpoint attracts scanning and credential attacks. Do not expose the
Node UI, Local Bridge socket or a Machine-mode Node by binding unrelated
services to the tunnel.

## Recommended default

1. Use `share --project . --tunnel none` first.
2. Keep the Hub and Node on loopback or a trusted LAN while testing.
3. Use a new temporary profile and a strong, unique account password.
4. Use a read-only Direct Key or OAuth scope until a write is needed.
5. Start with the safe inspection prompt and review every write, shell, Git,
   browser, artifact and AI action.
6. Use `--tunnel cloudflare` or `--tunnel ngrok` only for a cloud client, and
   revoke credentials after the demo.
7. Choose Machine mode only for private workflows where the whole OS-user
   boundary is acceptable.
