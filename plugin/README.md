# oc-remote

OpenCode v2 plugin for a self-hosted relay. Work with the sessions of your
local OpenCode server from a phone or browser: the agent, files, tools and MCP
servers stay on your machine; only HTTP, SSE and permission signals cross the
tunnel.

## Install

The relay administrator creates a tenant and a device key. On the laptop:

```sh
npx oc-remote join https://your-relay.example ocr_...
```

The command verifies the relay and the key, then adds one entry to the global
`~/.config/opencode/opencode.json`:

```jsonc
{
  "plugins": [
    {
      "package": "oc-remote",
      "options": { "url": "https://your-relay.example", "key": "ocr_..." }
    }
  ]
}
```

Comments, formatting and unrelated plugins are preserved; a `.bak` copy is
written once.

Then in the OpenCode TUI:

```
/remote          # status + a new pair link, copied to the clipboard
/remote off      # close the tunnel
/remote on       # reopen it
```

Open the pair link on the phone. It is single use, expires in 5 minutes, and
the browser keeps a 7-day sliding session. The official OpenCode web UI gets a
small **Remote** button with "Connect device", "Sign out everywhere", Web Push
notifications and an agent picker.

## Requirements

- OpenCode v2.2.x.
- Node.js 20+ for the `join` CLI.
- A running `oc-remote-relay` (Go/Docker) and its URL/device key.

## How it works

- The plugin runs inside the OpenCode server and keeps one outbound WebSocket
  per process, with a process-wide refcount for multi-location setups.
- Pair codes are minted from the local `POST /api/pair`; the relay redeems them
  server-side and stores the OpenCode token. The browser never receives an
  OpenCode cookie.
- Permission events are forwarded from the server event stream; duplicates are
  deduplicated by event ID.

## Security

- Pair code: 22 chars, single use, 5 minutes.
- Browser session: `__Host-ocr_session`, `Secure`, `HttpOnly`, `SameSite=Lax`,
  7 days sliding.
- No inbound ports on the laptop; the tunnel is outbound WSS with a device key.
- The relay stores metadata only, no prompt or response bodies.
- Treat a pair link as a credential: it gives control of that device until it
  is used or expires.

## Links

- Repository: <https://github.com/RacFor4k/oc-remote>
- Specification: <https://github.com/RacFor4k/oc-remote/blob/main/SPEC.md>

## License

MIT
