# HTTPS via mkcert

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
```

`data/certs/` is bind-mounted read-only into the container at
`/home/bosun/certs` (`docker-compose.yml`), and `config.yaml`'s
`web.tls_cert_file`/`web.tls_key_file` point there. `internal/webui.Server.Serve`
serves HTTPS when both are set, plain HTTP otherwise (`internal/config`'s
defaults keep both empty, so every deployment predating this feature is
unaffected).

Both `config.yaml` and `data/` are gitignored — the cert/key never get
committed, and each deployment generates its own.

## Trusting the CA on a client device

`mkcert -install` only trusts the CA on the machine mkcert ran on. Every
*other* device that connects (a phone, a laptop) needs the CA's public
cert imported and trusted there too — this is a one-time step per device,
not per site, since one CA can vouch for every cert it issues:

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
rerun the `mkcert -cert-file ... -key-file ...` command above with the new
address, then `docker compose restart bosun`. No device needs to re-trust
anything; the CA itself didn't change.
