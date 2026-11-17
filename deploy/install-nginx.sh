#!/usr/bin/env bash
set -euo pipefail

domain="rc.addies.ai"
src="/opt/oc-remote/deploy/nginx-${domain}.conf"
dst="/etc/nginx/sites-available/${domain}"

if [ ! -f "/etc/letsencrypt/live/${domain}/fullchain.pem" ]; then
  echo "certificate for ${domain} is missing; run certbot first" >&2
  exit 1
fi

install -m 0644 "${src}" "${dst}"
ln -sf "${dst}" "/etc/nginx/sites-enabled/${domain}"
nginx -t
systemctl reload nginx
echo "installed ${domain}"
