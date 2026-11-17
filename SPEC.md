# oc-remote — specification

Remote control of a local OpenCode v2 server from a phone or browser, through a
self-hosted relay on addiesVPS. Multi-tenant, pair-code auth, push on permission
requests. Target: OpenCode 2.2.x, Linux relay, Windows/macOS/Linux plugin.

## 1. Goals

- Open the official OpenCode web UI on a phone and work with every session of a
  local OpenCode server: read history, send prompts, answer permission
  requests, switch model and agent, create sessions.
- Keep execution, files, tools and MCP servers on the laptop. Never expose the
  OpenCode server directly to the internet.
- One relay serves several colleagues. Each colleague has a tenant, one or more
  devices, and an isolated browser session bound to a device.
- Pair once per browser: 5-minute single-use code, then a 7-day sliding session
  with silent upstream token renewal while the device is online.
- Metadata-only relay: no prompt or response bodies in the database or logs.

## 2. Non-goals (v1)

- No static link, no QR code. The pair link is short-lived and single-use.
- No custom chat UI or mobile skin; the official web UI is reused.
- No WebSocket proxying; the web UI uses SSE and REST only.
- No full push set; only `permission.asked`.
- No self-service signup; tenants are created by an admin command.
- No admin reading of sessions; only `disable` / `enable` / `rm`.
- No transcript storage, analytics, or billing.

## 3. Topology

```
phone browser --HTTPS--> nginx (rc.addies.ai) --> relay 127.0.0.1:4097
                                                     ^
                                                     | outbound WSS, device key
                                                     |
laptop: OpenCode server + oc-remote plugin  <--------+
          ^ HTTP 127.0.0.1:<port> (Basic token)
```

- The relay terminates no TLS itself; nginx does.
- The plugin runs inside the OpenCode server process. It keeps the tunnel,
  mints pair codes, proxies HTTP to `127.0.0.1:<port>`, and forwards
  permission events.
- The browser sees only the relay. OpenCode cookies never reach the browser.

## 4. Components

| Path       | What                                                                 |
|------------|----------------------------------------------------------------------|
| `relay/`   | Go binary: public HTTP, WS tunnel, auth, proxy, push, admin          |
| `plugin/`  | npm package `oc-remote`: server part, TUI part, RPC, `join` CLI      |
| `deploy/`  | Dockerfile, compose, nginx block, install script                     |
| `web/`     | Embedded panel, inject script, notify page, service worker, offline  |

## 5. Flows

### 5.1 Onboarding

```
$ oc-remote-relay admin add alice --device alice-laptop
tenant: alice
device: alice-laptop
key:    ocr_...
join:   npx oc-remote join https://rc.addies.ai ocr_...
```

`join`:

1. Normalizes the URL, checks `GET /_ocr/healthz`.
2. Verifies the key with `POST /_ocr/api/verify` (`Authorization: Bearer`).
3. Edits `~/.config/opencode/opencode.json` (or `$XDG_CONFIG_HOME`) with
   `jsonc-parser`; adds or replaces:
   `{"package":"oc-remote","options":{"url":"...","key":"...","name":"..."}}`
   in the `plugins` array. Unrelated settings are preserved. A `.bak` copy is
   written once.
4. Prints the next step: run `/remote` in the TUI.

Plugin loads, reads `options`, connects the tunnel. No restart is required;
`/reload` forces a reload if the server missed the config change.

### 5.2 Pair

1. TUI `/remote` or panel "Connect device" asks the plugin to mint.
2. Plugin: `POST {service.url}/api/pair` with Basic
   `opencode:<service.json password>` → `{code, expires_in}`.
3. Plugin registers the code with the relay (`pair.register` or `pair.minted`).
4. URL: `https://rc.addies.ai/_ocr/pair/<code>`.
5. Browser opens the URL. Relay consumes the code, sends
   `GET /auth/connect/<code>` with `Accept: application/json` through the
   tunnel → `{token}`.
6. Relay creates a session, stores the token encrypted, sets
   `__Host-ocr_session`, redirects to `/`.
7. The browser loads the official web UI through the relay.

Pair codes: 22 chars base64url, TTL ≤ 300 s, single use, stored as SHA-256.
The relay enforces expiry and single use even if the upstream does not.

### 5.3 Proxying

1. Cookie → session row → device and token.
2. Relay sends a `req` frame (method, path+query, filtered headers, body, auth).
3. Plugin performs the request against the local service with
   `Authorization: Basic opencode:<token>`.
4. Plugin streams `res.start`, `res.chunk`, `res.end`; the relay writes and
   flushes each chunk. SSE is never buffered.
5. HTML responses are buffered (≤2 MB) and get
   `<script src="/_ocr/inject.js?v=<version>" defer></script>` before `</head>`.
6. Upstream 401 → mint + redeem a fresh pair code, replace the token, retry
   once. If the device is offline, return the offline page.

Stripped request headers: `Host`, `Cookie`, `Authorization`, `Connection`,
`Keep-Alive`, `Proxy-*`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`,
`Accept-Encoding`, `Content-Length`.
Stripped response headers: `Set-Cookie`, `Content-Length`,
`Transfer-Encoding`, `Connection`, `Keep-Alive`.

### 5.4 Logout

- "Sign out everywhere": delete every session of the tenant, clear the cookie.
- `disable <tenant>`: mark the tenant disabled, delete its sessions, kick its
  tunnels. The admin cannot read session content.
- `rm <tenant>`: delete the tenant, devices, sessions, push subscriptions.

### 5.5 Push

- Plugin subscribes to `permission.asked`; sends
  `{t:"notify", kind:"permission", sessionID, requestID, action}`.
- Relay sends Web Push to every subscription of the tenant.
- Notification text: "OpenCode needs permission" + action. No prompt content.
- Click opens `https://rc.addies.ai/`.
- Subscription page `/_ocr/notify`; service worker `/_ocr/sw.js`, scope
  `/_ocr/`; VAPID keys generated on first boot and stored in the DB.
- iOS: the site must be added to the home screen; documented, not automated.

## 6. Relay HTTP surface

| Method | Path                        | Auth              | Notes                          |
|--------|-----------------------------|-------------------|--------------------------------|
| GET    | `/_ocr/healthz`             | none              | `{ok, version}`                |
| GET    | `/_ocr/pair/{code}`         | none              | redeem, set cookie, 302 `/`    |
| GET    | `/_ocr/`                    | session           | control panel                  |
| GET    | `/_ocr/panel.css`           | none              | static                         |
| GET    | `/_ocr/panel.js`            | none              | static                         |
| GET    | `/_ocr/inject.js`           | none              | injected into the web UI       |
| GET    | `/_ocr/notify`              | session           | enable push                    |
| GET    | `/_ocr/sw.js`               | none              | push service worker            |
| GET    | `/_ocr/offline`             | none              | offline page                   |
| POST   | `/_ocr/api/pair`            | session + CSRF    | `{url, expiresAt}`             |
| POST   | `/_ocr/api/logout-all`      | session + CSRF    | 204                            |
| GET    | `/_ocr/api/status`          | session           | device, tenant, online, push   |
| GET    | `/_ocr/api/agents`          | session           | proxied `/api/agent`           |
| POST   | `/_ocr/api/agent`           | session + CSRF    | `{sessionID, agent}` → 204     |
| GET    | `/_ocr/api/push/key`        | session           | VAPID public key               |
| POST   | `/_ocr/api/push/subscribe`  | session + CSRF    | subscription JSON              |
| POST   | `/_ocr/api/push/unsubscribe`| session + CSRF    | `{endpoint}`                   |
| POST   | `/_ocr/api/verify`          | device key        | used by `join`                 |
| GET    | `/_ocr/tunnel`              | device key (query)| WebSocket upgrade              |
| *      | everything else             | session           | proxy to the device            |

CSRF: `SameSite=Lax` cookie + `X-OCR-CSRF: 1` + same-origin `Origin` on every
POST under `/_ocr/api`.

Admin API (loopback only, `127.0.0.1:4098`, `Authorization: Bearer`):
`POST /admin/tenant/add`, `/remove`, `/disable`, `/enable`, `/rotate-key`;
`GET /admin/list`.

## 7. Tunnel protocol

One WebSocket per device. JSON frames; bodies base64. Max WS payload 64 MB;
request bodies limited to 25 MB.

| Type          | Direction       | Fields                                              |
|---------------|-----------------|-----------------------------------------------------|
| `hello`       | relay → plugin  | `device`, `tenant`, `version`                       |
| `hello.ack`   | plugin → relay  | `version`                                           |
| `ping`/`pong` | both            | none                                                |
| `req`         | relay → plugin  | `id`, `method`, `path`, `headers`, `auth`, `body?`  |
| `res.start`   | plugin → relay  | `id`, `status`, `headers`                           |
| `res.chunk`   | plugin → relay  | `id`, `body` (base64)                               |
| `res.end`     | plugin → relay  | `id`                                                |
| `res.error`   | plugin → relay  | `id`, `error`                                       |
| `abort`       | relay → plugin  | `id`                                                |
| `pair.mint`   | relay → plugin  | `id`                                                |
| `pair.minted` | plugin → relay  | `id`, `code`, `expires_in`, `url`                   |
| `pair.register`| plugin → relay | `code`, `expires_in`, `url`                         |
| `notify`      | plugin → relay  | `kind`, `sessionID`, `requestID`, `action`          |

Reconnect: plugin exponential backoff 1 s → 30 s with jitter; any open request
fails and the browser retries. Relay keeps device state in memory, sessions in
SQLite. Tunnel URL uses `?key=<device-key>`; nginx disables access logs for
`/_ocr/tunnel` and `/_ocr/pair/`.

## 8. Database (SQLite, WAL, pure Go driver)

```sql
CREATE TABLE meta(key TEXT PRIMARY KEY, value BLOB NOT NULL);

CREATE TABLE tenants(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE devices(
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  key_hash BLOB NOT NULL UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_seen INTEGER
);

CREATE TABLE sessions(
  id_hash BLOB PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  token BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ua TEXT
);

CREATE TABLE pair_codes(
  code_hash BLOB PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  used_at INTEGER
);

CREATE TABLE push_subs(
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  endpoint TEXT NOT NULL UNIQUE,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
```

- Tokens encrypted AES-GCM with `data/secret.key` (32 bytes, 0600).
- `last_seen` / `expires_at` update at most once per minute (sliding 7 days).
- Cleanup: expired sessions and pair codes at startup and hourly.
- No message content, no IP addresses; optional truncated UA only.

## 9. Security

- Pair code: 22 chars, single use, 5 min, hashed at rest, rate-limited.
- Browser session: 32 random bytes, SHA-256 at rest, `__Host-ocr_session`
  (`Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`), 7 days sliding.
- Upstream token: 30-day OpenCode token, encrypted at rest, only used by the
  relay; never sent to the browser.
- Rotation: `logout-all` deletes sessions; upstream tokens become unreachable.
  Changing the OpenCode password kills every upstream token.
- TLS only; HSTS; no plain HTTP endpoint for `rc.addies.ai`.
- No request bodies, codes, keys or tokens in logs.
- Admin API on loopback with a 32-byte token from `data/admin.token`.
- The OpenCode server stays on `127.0.0.1`; only the tunnel reaches it.

## 10. Configuration (relay)

| Env                          | Default                 | Notes                                  |
|------------------------------|-------------------------|----------------------------------------|
| `OC_REMOTE_PUBLIC_URL`       | `https://rc.addies.ai`  | used for pair URLs and cookies         |
| `OC_REMOTE_LISTEN`           | `127.0.0.1:4097`        | public HTTP                            |
| `OC_REMOTE_ADMIN_LISTEN`     | `127.0.0.1:4098`        | admin HTTP                             |
| `OC_REMOTE_DATA_DIR`         | `/data`                 | SQLite, keys, admin token              |
| `OC_REMOTE_SESSION_TTL`      | `168h`                  | sliding browser session                |
| `OC_REMOTE_MAX_BODY`         | `26214400`              | 25 MB                                  |
| `OC_REMOTE_INSECURE_COOKIES` | `0`                     | local dev only                         |
| `OC_REMOTE_VAPID_SUBJECT`    | `mailto:admin@<host>`   | Web Push subject                       |
| `OC_REMOTE_LOG_LEVEL`        | `info`                  | `debug` adds metadata only             |

## 11. Deployment

1. DNS: `rc.addies.ai A 62.83.87.93` (owner action; API access not required).
2. TLS: `certbot certonly --webroot -w /opt/mailcow-dockerized/data/web -d rc.addies.ai`
   (verified: port 80 serves `/.well-known/acme-challenge/` for any Host).
3. nginx: new `rc.addies.ai` server block, `proxy_pass http://127.0.0.1:4097`,
   `proxy_buffering off`, `proxy_read_timeout 1h`, WS upgrade for
   `/_ocr/tunnel`, `access_log off` for `/_ocr/tunnel` and `/_ocr/pair/`.
   Back up the config, `nginx -t`, reload.
4. `docker compose up -d --build` in `/opt/oc-remote`; volume `./data:/data`;
   only `127.0.0.1:4097` published; admin port stays inside the container.
5. `docker compose exec relay oc-remote-relay admin add alice`.
6. Install the plugin on the laptop: `npx oc-remote join ...`, then `/remote`.

The VPS is a production host (mailcow, addy.ai). Do not touch existing
containers, ports 80/25-series/8000/5432/6379, or the mailcow nginx. The relay
is a new tiny container behind the existing host nginx. Disk cleanup is
approved but not required for this image.

## 12. Acceptance tests

1. `admin add` → `join` → tunnel shows online in `/remote`.
2. Pair link → cookie set → official UI loads; session list matches the laptop.
3. Prompt from the phone → assistant answer streams; no buffering.
4. Permission request → push arrives → reply `once` → agent continues.
5. Model switch works; agent picker switches the agent of the current session.
6. "Sign out everywhere" → every browser gets 401; a new pair link works.
7. Laptop offline → branded offline page; reconnect → the same session works.
8. Session older than 7 days idle → reconnect page; a new pair link works.
9. Alice and Bob cannot see each other's sessions; sessions are device-bound.
10. `admin disable alice` → tunnel kicked, sessions dead, admin sees no content.
11. Logs and DB contain no prompt bodies, pair codes, keys or tokens.
12. SSE stays open ≥10 minutes through nginx; no proxy buffering.

## 13. Known risks

- TUI link display: the v2 TUI dialog/wrap behavior is not verified; fallback is
  the laptop `/_ocr/` panel with a copy button.
- Auto-renew requires the laptop online; otherwise the reconnect page appears.
- `permission.asked` availability inside the server plugin is unverified;
  fallback is parsing the SSE stream in the relay.
- Workbox caches the UI HTML; the injected script may not appear until the
  cache revalidates. The panel is reachable directly at `/_ocr/` regardless.
- OpenCode API drift: pin `@opencode/plugin@2.2.5`; re-test on upgrades.
- Pair link prefetch or double-open consumes the code once; the second open
  shows "link used, mint a new one".
- Multi-device UI selection is out of scope; each browser session is bound to
  one device.
