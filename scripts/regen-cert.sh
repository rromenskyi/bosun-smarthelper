#!/usr/bin/env bash
# Regenerate the mkcert leaf certificate (data/certs/cert.pem + key.pem) for
# this host's current LAN IP, then restart bosun so it picks it up. Run this
# whenever the host's IP changes (DHCP reassignment, new router) — the CA
# itself doesn't change, so no device needs to re-trust anything. See
# docs/tls.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="$REPO_ROOT/data/certs"

if ! command -v mkcert >/dev/null 2>&1; then
	echo "mkcert not found — see docs/tls.md's one-time setup section." >&2
	exit 1
fi

LAN_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") print $(i+1)}')"
if [ -z "$LAN_IP" ]; then
	echo "Could not determine the current LAN IP (no default route?)." >&2
	exit 1
fi

MDNS_HOST="$(hostname | tr '[:upper:]' '[:lower:]').local"
# bosunonline.us resolves straight to the LAN IP for local clients (see
# docs/cloudflare.md's split-horizon DNS override on the router) instead of
# going out through the Cloudflare tunnel — but that only avoids a
# certificate warning if this leaf cert also covers that name, since it's
# still this mkcert cert answering the TLS handshake, not Cloudflare's.
PUBLIC_DOMAIN="bosunonline.us"

echo "Regenerating cert for: $LAN_IP $MDNS_HOST $PUBLIC_DOMAIN localhost 127.0.0.1 ::1"
mkdir -p "$CERT_DIR"
mkcert -cert-file "$CERT_DIR/cert.pem" -key-file "$CERT_DIR/key.pem" \
	"$LAN_IP" "$MDNS_HOST" "$PUBLIC_DOMAIN" localhost 127.0.0.1 ::1

if [ ! -f "$CERT_DIR/rootCA.pem" ]; then
	cp "$(mkcert -CAROOT)/rootCA.pem" "$CERT_DIR/rootCA.pem"
	echo "Copied rootCA.pem (was missing)."
fi

echo "Cert written to $CERT_DIR/cert.pem"

if command -v docker >/dev/null 2>&1 && [ -f "$REPO_ROOT/docker-compose.yml" ]; then
	echo "Restarting bosun to pick up the new cert..."
	(cd "$REPO_ROOT" && sudo docker compose restart bosun)
else
	echo "Restart bosun manually to pick up the new cert: docker compose restart bosun"
fi
