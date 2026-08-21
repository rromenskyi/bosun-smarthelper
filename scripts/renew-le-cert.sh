#!/usr/bin/env bash
# Renew the real Let's Encrypt certificate for bosunonline.us (DNS-01 via
# Cloudflare) and restart bosun, but only if a renewal actually happened —
# lego's own --renew-hook only fires on an actual renewal, not a no-op
# check. Meant to run daily (cron/systemd timer); lego itself decides
# whether the cert is close enough to expiry (--days) to bother. See
# docs/cloudflare.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LEGO="${LEGO_BIN:-$HOME/go/bin/lego}"

if [ ! -x "$LEGO" ]; then
	echo "lego not found at $LEGO — go install github.com/go-acme/lego/v4/cmd/lego@latest" >&2
	exit 1
fi

set -a
source "$REPO_ROOT/.env"
set +a

# --dns.resolvers: this host's own DNS resolver answers bosunonline.us
# with the LAN IP directly (docs/cloudflare.md's split-horizon override),
# which breaks lego's own apex-zone detection for that exact name if left
# on the system default — route lego's lookups around it via a public
# resolver instead.
"$LEGO" \
	--accept-tos --email "$LEGO_EMAIL" \
	--dns cloudflare --dns.resolvers 1.1.1.1:53 \
	--domains bosunonline.us --path "$REPO_ROOT/data/certs/le" \
	renew --days 30 \
	--renew-hook "cd '$REPO_ROOT' && sudo -n docker compose restart bosun"
