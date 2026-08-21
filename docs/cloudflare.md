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

## Access is confirmed configured

An unauthenticated request to `https://bosunonline.us/*` gets a `302` to
`https://roman220.cloudflareaccess.com/cdn-cgi/access/login/bosunonline.us`
— confirmed live, not just assumed from the dashboard — so a Cloudflare
Access application/policy is in front of the hostname and bosun itself is
never reached without passing it first. (The DNS-scoped API token still
can't read the Access app/policy config itself — `GET
/accounts/.../access/apps` returns "Authentication error" — so if the
policy ever needs changing, that's still a dashboard-only edit: Zero Trust
→ Access → Applications.)

## TLS issuance took a few tries

A freshly added zone's Cloudflare-edge certificate (SSL/TLS → Edge
Certificates → Universal, covering `bosunonline.us` and `*.bosunonline.us`)
is normally supposed to finish within minutes of nameserver activation, up
to ~24h in rare cases. On this zone it didn't move past "Pending
Validation" on its own; toggling the Universal SSL switch off and back on
made the certificate disappear entirely ("No certificates" listed) rather
than restart cleanly, and it took a second off/on cycle before it actually
came back and issued successfully. If this happens again: don't repeatedly
toggle — one off/on cycle, then leave it alone; toggling mid-issuance seems
to be what got it stuck, not what fixed it. `http://bosunonline.us` working
and 301-redirecting to `https://` the whole time was the useful signal that
DNS/zone/tunnel were never the problem — only edge-cert issuance was.

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
