# HTTPS via mkcert

**This deployment no longer uses any of this.** Once a real domain
(`bosunonline.us`) and Cloudflare entered the picture, a real
publicly-trusted Let's Encrypt certificate became available via DNS-01 —
see `docs/cloudflare.md` — which needs no per-device CA install at all, on
top of a real cert, so it replaced mkcert here entirely: no more
`web.ca_cert_file`, no `/ca.pem` download route, and raw LAN-IP/`.local`
access was deliberately dropped in favor of always using
`https://bosunonline.us` (works both locally, via split-horizon DNS, and
remotely, via the tunnel). Everything below is kept as generic guidance for
a deployment that has no domain to fall back on — that's still a real,
supported path through `internal/webui.Server.Serve`/`ValidateBind`, just
not what this specific host runs anymore.

---

The web UI is a LAN-only appliance bound to a private IP, not a public
domain — a normal CA (Let's Encrypt etc.) can't issue a cert for that, and a
plain self-signed one makes every browser show a "not secure" warning
forever. [mkcert](https://github.com/FiloSottile/mkcert) solves this by
acting as your own locally-trusted CA: install its root once on each device
you use, and certs it issues for your LAN IP/hostname are trusted with no
warning, same as a real one.

## One-time setup (already done on this host)

```
sudo apt-get install mkcert libnss3-tools
mkcert -install                 # creates the local CA under ~/.local/share/mkcert
mkdir -p data/certs
mkcert -cert-file data/certs/cert.pem -key-file data/certs/key.pem \
  10.0.0.111 roman220-macmini5-1.local localhost 127.0.0.1 ::1
cp "$(mkcert -CAROOT)/rootCA.pem" data/certs/rootCA.pem
```

`data/certs/` is bind-mounted read-only into the container at
`/home/bosun/certs` (`docker-compose.yml`), and `config.yaml`'s
`web.tls_cert_file`/`web.tls_key_file`/`web.ca_cert_file` point there.
`internal/webui.Server.Serve` serves HTTPS when the first two are set,
plain HTTP otherwise (`internal/config`'s defaults keep all three empty,
so every deployment predating this feature is unaffected).

Both `config.yaml` and `data/` are gitignored — the cert/key never get
committed, and each deployment generates its own.

## Trusting the CA on a client device

`mkcert -install` only trusts the CA on the machine mkcert ran on. Every
*other* device that connects (a phone, a laptop) needs the CA's public
cert imported and trusted there too — this is a one-time step per device,
not per site, since one CA can vouch for every cert it issues.

### Downloading it directly from the running service

Set `web.ca_cert_file` in `config.yaml` to the CA's public cert (this
host's is `/home/bosun/certs/rootCA.pem` inside the container, copied from
mkcert's `rootCA.pem` — **never** point this at `rootCA-key.pem`, the
CA's private key). Once set, the settings page (gear icon) shows a
"download the HTTPS certificate" link, and it's also reachable directly at
`https://<host>/ca.pem` — so a new device can grab it with nothing more
than a browser, no separate file transfer needed. You'll see a
certificate warning the first time you visit, since the very cert you're
about to trust is what's serving the page — click through it (Safari:
"Show Details" → "visit this website"); that's expected and only needed
this once.

If you'd rather not expose the download route at all, leave
`ca_cert_file` empty (the default) and transfer the file manually instead:

```
mkcert -CAROOT   # prints the directory; the file to copy is rootCA.pem
```

Copy that `rootCA.pem` to the device (AirDrop, email to yourself, USB,
whatever's convenient) and trust it:

- **Android**: Settings → Security → Encryption & credentials → Install a
  certificate → CA certificate. Chrome uses the OS trust store, so this
  covers it; Firefox on Android instead needs the cert imported through
  its own Settings → "Install certificate from SD card" (Firefox ships its
  own trust store on some versions).
- **iOS**: AirDrop or email the file to the device, open it (Settings
  prompts "Profile Downloaded"), install it under Settings → General →
  VPN & Device Management, **then** separately enable full trust: Settings
  → General → About → Certificate Trust Settings → toggle it on. Both
  steps are required — installing the profile alone doesn't grant trust
  for TLS.
- **macOS**: double-click the file to open it in Keychain Access, find it
  under "System" (or "login"), open it, expand "Trust," and set "When
  using this certificate" to "Always Trust."
- **Windows**: double-click the file → Install Certificate → Local
  Machine → place it directly in "Trusted Root Certification
  Authorities."
- **Linux desktop**: run mkcert itself with the same `-install` command,
  or `sudo cp rootCA.pem /usr/local/share/ca-certificates/mkcert.crt &&
  sudo update-ca-certificates`, plus reimport into Firefox/Chrome's own
  NSS store the same way this host's setup did.

## Devices that can never trust the CA (corporate MDM)

A corporate-managed phone's MDM profile often blocks installing custom
root certs outright — no amount of retrying the steps above will get past
that, since it's an intentional restriction, not a bug in the steps.

For a device like that, set `web.http_fallback_bind` to a second address
(e.g. `10.0.0.111:8080`) — once TLS is enabled, `Server.Serve` starts a
second, plain-HTTP listener there with the exact same handler, alongside
the TLS one on the primary `web.bind`. That device just uses
`http://10.0.0.111:8080` with zero certificate friction, exactly as every
device did before TLS was set up; every other device keeps using the
trusted `https://` address on the primary port. The fallback is ignored
entirely unless `web.tls_cert_file`/`tls_key_file` are also set — leaving
it configured costs nothing on a deployment that never turns TLS on.

## Using the standard port (443)

`web.bind`'s port is just a config value — nothing stops it being `443`
instead of `8080` so the URL doesn't need `:8080` typed on every device.
The one catch: ports below 1024 normally require root, and the container
runs as a non-root user (`bosun`, uid 1000 — see Dockerfile) precisely so a
compromised process can't do much. Rather than run as root or juggle Linux
capabilities (which for a non-root process also need `setcap` baked into
the binary, not just Docker's `cap_add` — tried and confirmed insufficient
on its own), this host instead lowers the kernel's unprivileged-port
threshold, which under `network_mode: host` (used here — see
`docker-compose.yml`) applies directly since the container shares the
host's network namespace:

```
echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/99-bosun-unprivileged-ports.conf
sudo sysctl --system
```

This only affects ports 443–1023 (any process can now bind those without
root); 1–442 still need root as before. One-time, persists across
reboots.

## Regenerating after an IP change

If the host's LAN IP changes (DHCP reassignment, new router), the leaf
cert (not the CA) needs regenerating with the new IP in its SAN list —
rerun `scripts/regen-cert.sh`, which detects the host's current default-route
IP and mDNS hostname, regenerates `data/certs/cert.pem`/`key.pem` for them,
and restarts `bosun`. No device needs to re-trust anything; the CA itself
didn't change. (Equivalent to rerunning the `mkcert -cert-file ...
-key-file ...` command from the one-time setup above with the new address.)

## Binding to 0.0.0.0 instead of a fixed IP

`web.bind`/`http_fallback_bind` can be set to `0.0.0.0` instead of a literal
private IP (`webui.ValidateBind` allows both) — useful on a host without a
DHCP reservation, where the IP can change on every reboot and hand-editing
`config.yaml` each time isn't practical. The service still listens on every
interface's private address only in practice (no public IP is ever assigned
to this LAN-only host), and it still has no authentication, so this remains
strictly a trusted-LAN deployment either way.

## Reaching it from outside the LAN

Everything above is about the LAN-only path. For access from outside the
LAN, see `docs/cloudflare.md` — a separate, additive `cloudflared` tunnel to
a dedicated domain, **not** a change to this no-auth LAN setup. That path
needs its own authentication gate (Cloudflare Access) in front of it, since
nothing here provides any.

Binding to `0.0.0.0` does **not** remove the need to rerun
`scripts/regen-cert.sh` after an IP change — that only fixes what address
the *socket* listens on. The TLS cert's SAN list is still pinned to whatever
IP it was generated for, so browsers hitting the new IP directly will see a
certificate warning until the cert is regenerated. Connecting via the mDNS
hostname (`https://roman220-macmini5-1.local`, already in the cert's SAN)
avoids that entirely, since it resolves to whatever the current IP is.
