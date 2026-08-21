# Remote access via Cloudflare Tunnel

The web UI is otherwise LAN-only, no-auth, bound to `0.0.0.0` (see
`docs/tls.md`). To reach it from outside the LAN without forwarding any
ports on the router, `cloudflared` runs as an outbound-only connector to
Cloudflare's edge, and the domain `bosunonline.us` (bought for this project)
proxies through Cloudflare to it.

**`https://bosunonline.us` is now the one address, everywhere.** Raw
LAN-IP/`.local` access was deliberately retired in favor of always using
the domain — on this LAN it resolves straight to bosun via split-horizon
DNS (below), skipping Cloudflare entirely; from anywhere else it goes
through the tunnel. Same URL, same real certificate, either way.

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
  **Zone:DNS:Edit + Zone:Zone:Read** on this zone (see "Real TLS
  certificate via DNS-01" below for why both are needed) — used by `lego`
  to issue and renew the real cert. Verified valid via `GET
  /client/v4/user/tokens/verify`, but still has no permission to read
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

## Split-horizon DNS on the router

The LAN's router (a GL.iNet GL-SFT1200, OpenWrt 18.06, at `192.168.8.1`)
answers `bosunonline.us` with this host's own LAN IP instead of forwarding
the query out to real DNS — so on this network, visiting
`https://bosunonline.us` goes straight to bosun, skipping Cloudflare, the
tunnel, and the Access login entirely, and keeps working with no internet
at all. This host's LAN IP is already pinned via a static DHCP reservation
(`config host` in `/etc/config/dhcp`, matched by MAC — this predates this
setup, not something added for it), so the override doesn't go stale on a
lease renewal.

The override itself needed a real `address=` dnsmasq directive, not the
UCI `config domain` section's usual translation:

```
# /etc/dnsmasq.conf on the router (persistent — /etc survives reboots,
# unlike /tmp/dnsmasq.d, which this build's confdir points at)
address=/bosunonline.us/192.168.8.203
```

then `/etc/init.d/dnsmasq restart`. A `config domain` UCI entry (as used
for this router's own `console.gl-inet.com`) was tried first, but it
renders into `addn-hosts` (a plain hosts-file-style mapping) — that gave a
working A record but let AAAA queries fall through to real DNS, so
IPv6-preferring clients still resolved the public Cloudflare address and
went through the tunnel instead of direct. `address=/domain/ipv4` answers A
with that address *and* answers AAAA for the same name with an empty
response instead of forwarding it, so there's no per-family leak — confirm
with `resolvectl query bosunonline.us` after any change here, checking
that no `2606:...` (Cloudflare) address shows up alongside the LAN IP.

This override only affects devices that actually use this router as their
DNS resolver (the LAN default) — a device on this network with a hardcoded
resolver (1.1.1.1, 8.8.8.8, DNS-over-HTTPS in-browser) would skip the
router entirely and still go the long way through Cloudflare.

Because `bosunonline.us` is resolved locally to a different IP than
Cloudflare's, the TLS handshake for it on this LAN is answered by bosun's
own certificate, not Cloudflare's edge cert — which is exactly why that
certificate needed to be a real one too (below), not mkcert's: mkcert's
cert is only trusted on devices that installed its CA, defeating the point
of switching to a real domain in the first place.

## Real TLS certificate via DNS-01 (lego)

`bosun`'s own listener (`config.yaml`'s `web.tls_cert_file`/`tls_key_file`)
serves a real Let's Encrypt certificate for `bosunonline.us`, issued via
DNS-01 — no per-device CA install needed anywhere, unlike the mkcert setup
in `docs/tls.md` this replaced. [lego](https://github.com/go-acme/lego)
does the issuing (a static Go binary, `go install
github.com/go-acme/lego/v4/cmd/lego@latest` — no Python, matching this
project's voice-stack precedent).

Two things that didn't work on the first try:

- **The DNS-edit token alone wasn't enough.** lego's Cloudflare provider
  looks up which zone a domain belongs to via the Cloudflare API before it
  can create the challenge TXT record, and a token scoped to only
  `Zone:DNS:Edit` can't do that lookup — it failed with `failed to find
  zone us.: zone could not be found` (silently walking up to the bare TLD
  because the real zone lookup came back empty, not because anything about
  `bosunonline.us` itself was wrong). Fixed by adding **Zone:Zone:Read** to
  the same token.
- **This host's own split-horizon DNS override breaks lego's zone lookup**
  if left on the system resolver — lego also resolves `bosunonline.us`
  itself as part of finding the zone apex, and this host's resolver (the
  router) answers that with the LAN IP directly, not a real DNS chain,
  which confused the same lookup. Fixed with `--dns.resolvers 1.1.1.1:53`,
  routing lego's own lookups around the override entirely.

`scripts/renew-le-cert.sh` wraps `lego ... renew --days 30` with a
`--renew-hook` that restarts `bosun` (only fires on an actual renewal, not
a no-op check), both fixes baked in, and runs daily via cron (`crontab -l`
— `17 4 * * *`, logging to `data/le-renew.log`). The cert/account data
lives in `data/certs/le/` (gitignored, same as `data/certs/*.pem`).

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
