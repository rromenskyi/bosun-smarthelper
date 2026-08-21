# Remote access via Cloudflare Tunnel

The web UI is otherwise LAN-only, no-auth, bound to `0.0.0.0` (see
`docs/tls.md`). To reach it from outside the LAN without forwarding any
ports on the router, `cloudflared` runs as an outbound-only connector to
Cloudflare's edge, and the domain `bosunonline.us` (bought for this project)
proxies through Cloudflare to it.

**This is a separate, additive path — it doesn't change how the LAN works.**
Local devices keep hitting `https://<lan-ip>` / `.local` directly, same as
before; only requests that come in through `bosunonline.us` go through
Cloudflare and the tunnel.

## What's set up

- **Zone**: `bosunonline.us` added to Cloudflare (Free plan), nameservers
  pointed at Cloudflare (`malavika.ns.cloudflare.com`,
  `paul.ns.cloudflare.com`), zone status `active`.
- **Tunnel**: created in the dashboard (Zero Trust → Networks → Tunnels),
  named `bosun`. Runs as the `cloudflared` service in `docker-compose.yml`
  (`network_mode: host`, `restart: unless-stopped`), authenticated via
  `TUNNEL_TOKEN` from `.env` (`CLOUDFLARE_TUNNEL_TOKEN`, gitignored — never
  commit this).
- **Public hostname / ingress**: the tunnel's ingress config routes the
  **root domain** `bosunonline.us` → `http://localhost:8080` (plain HTTP,
  not `:443`) — Cloudflare's edge already terminates TLS for the public
  hop, so a second TLS handshake to loopback is pure overhead. No `bosun.`
  subdomain was set up; the whole domain is dedicated to this one project,
  so there was no reason to split it.
- **DNS record**: a proxied (orange-cloud) `CNAME bosunonline.us →
  <tunnel-id>.cfargotunnel.com`, created automatically alongside the
  ingress rule above.
- **DNS-edit API token**: `CLOUDFLARE_DNS_API_TOKEN` in `.env`, scoped to
  "Edit zone DNS" on this zone only. Not used yet — reserved for a future
  DNS-01 ACME challenge (a real Let's Encrypt cert for `bosunonline.us`,
  replacing the mkcert setup in `docs/tls.md` for anything that needs a
  publicly-trusted cert). Verified valid via
  `GET /client/v4/user/tokens/verify`, but has no permission to read
  Access/Tunnel config or SSL certificate-pack status — those need a
  broader token or the dashboard itself.

## Known gap — Access is not confirmed configured

**Nothing has verified a Cloudflare Access application/policy exists for
this hostname.** Without one, the moment Cloudflare's edge certificate for
`bosunonline.us` finishes issuing, this tunnel exposes bosun — no
authentication, memos, documents, GPS, everything — to the entire internet,
not just to us. The DNS-scoped API token can't check or create Access apps
(confirmed: `GET /accounts/.../access/apps` returns "Authentication error"
with this token's permissions), so this has to be verified/created by hand
in the dashboard:

Zero Trust → **Access → Applications → Add an application → Self-hosted**.
Application domain `bosunonline.us`. Policy → Include → Emails → your
email → Action Allow. Login method: One-time PIN is the simplest to start.

**Do this before relying on the tunnel for anything** — until it's
confirmed, treat `https://bosunonline.us` as publicly reachable with no
gate in front of it.

## TLS status while Universal SSL is issuing

A freshly added zone's Cloudflare-edge certificate (SSL/TLS → Edge
Certificates → Universal, covering `bosunonline.us` and `*.bosunonline.us`)
can take minutes to ~24h to finish issuing after nameserver activation.
Until then, `https://bosunonline.us` fails the TLS handshake at Cloudflare's
edge itself (`SSL alert number 40` / `handshake failure`) — plain
`http://bosunonline.us` still works and 301-redirects to `https://`, which
is actually a useful signal: it means DNS, the zone, and the tunnel route
are all fine, and the only thing pending is edge-cert issuance. No config
change fixes this faster; it's Cloudflare's own validation, "no action
required" per their own dashboard copy.

## Operational notes

- Tunnel health: `docker compose logs cloudflared` — look for `Registered
  tunnel connection` (one per edge connection, normally 4) and the
  connectivity pre-check summary (`Environment is healthy`).
- Changing the ingress mapping (e.g. adding a `bosun.` subdomain after
  all, or pointing at a different local port) is done in the dashboard —
  Zero Trust → Networks → Tunnels → the `bosun` tunnel → Public Hostname —
  not in this repo; `docker-compose.yml`'s `cloudflared` service only holds
  the connector, not the routing rules (those live in Cloudflare's tunnel
  config, fetched at connect time via the token).
- Rotating the tunnel token: Zero Trust → Networks → Tunnels → the `bosun`
  tunnel → "..." → Refresh connector token (or delete/recreate the tunnel),
  then update `CLOUDFLARE_TUNNEL_TOKEN` in `.env` and `docker compose up -d
  cloudflared`.
