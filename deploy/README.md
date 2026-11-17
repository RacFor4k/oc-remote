# Deploy

Target: addiesVPS (`62.83.87.93`, root). Existing host nginx owns 443; port 80
is mailcow's nginx. The relay listens on `127.0.0.1:4097` and is proxied by a new
`rc.addies.ai` server block. Nothing else on the host is touched.

## 1. DNS

At Spaceship add:

```
rc.addies.ai A 62.83.87.93
```

Wait until `nslookup rc.addies.ai` returns the VPS address.

## 2. Source

From the repo root:

```sh
ssh addiesVPS "mkdir -p /opt/oc-remote"
scp -r go.mod go.sum cmd internal web deploy .dockerignore addiesVPS:/opt/oc-remote/
```

## 3. Container

```sh
ssh addiesVPS
cd /opt/oc-remote
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml ps
curl -s http://127.0.0.1:4097/_ocr/healthz
```

The relay stores SQLite, VAPID keys, `secret.key` and `admin.token` in the
`oc-remote-data` named volume.

## 4. Certificate

Port 80 is served by mailcow's nginx; its webroot works for any Host (verified).

```sh
certbot certonly --webroot -w /opt/mailcow-dockerized/data/web \
  -d rc.addies.ai --non-interactive --agree-tos --email racfor4@gmail.com
```

## 5. nginx

```sh
bash /opt/oc-remote/deploy/install-nginx.sh
curl -s https://rc.addies.ai/_ocr/healthz
```

## 6. Tenant

```sh
cd /opt/oc-remote
docker compose exec -T relay /usr/local/bin/oc-remote-relay admin add vadim --device laptop
```

The command prints the device key and the `npx oc-remote join ...` one-liner.

## 7. Laptop

```sh
npx oc-remote join https://rc.addies.ai <key>
```

Before the npm package is published, use the local package:

```sh
node plugin/bin/oc-remote.mjs join https://rc.addies.ai <key> \
  --package file:///D:/Source/oc-remote/plugin --name laptop
```

Then run `/remote` in the OpenCode TUI; the link is copied to the clipboard.

## Notes

- `admin list`, `admin disable <tenant>`, `admin rm <tenant>`,
  `admin rotate-key <tenant> [--device name]` run through
  `docker compose exec -T relay /usr/local/bin/oc-remote-relay ...`.
- The relay's admin API is loopback-only inside the container.
- nginx access logs are off for `rc.addies.ai`, so pair codes never land in
  the access log; the relay logs metadata only.
- Do not touch `addy`, `mailcow`, AdGuard or the GPU routers on this host.
