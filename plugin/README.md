# oc-remote plugin

OpenCode v2 plugin for the oc-remote relay.

```sh
npx oc-remote join https://rc.addies.ai ocr_...
```

Then run `/remote` in the TUI. `/remote off` closes the tunnel, `/remote on`
reopens it.

Options are written into the global `opencode.json`:

```jsonc
{
  "plugins": [
    {
      "package": "oc-remote",
      "options": { "url": "https://rc.addies.ai", "key": "ocr_..." }
    }
  ]
}
```

The server part keeps the outbound WebSocket tunnel and mints pair codes from
the local `service.json`. The TUI part only talks to the server RPC.
