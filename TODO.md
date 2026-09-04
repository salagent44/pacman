# TODO: put pacman on prod

Right now pacman only runs as a systemd service on a local test VM.
Nothing below is done yet.

- [ ] **Pick and register the new domain** for this service and point an A (and AAAA)
      record at the prod box.
- [ ] **Install on the VPS.** Two commands, see "Install on a VPS" in README.md:

      ```sh
      scp ./pacman vps:
      ssh vps 'sudo ./pacman serve -install'
      ```

      It prints the token once. Save it in the password manager right away.
- [ ] **TLS in front.** Plain HTTP on the public internet leaks the token on the wire.
      Preferred: Caddy on 80/443 reverse-proxying to `127.0.0.1:8080`
      (automatic certificates). nginx + certbot works too.
- [ ] **Bind to localhost once the proxy is up:** set `PACMAN_ADDR=127.0.0.1:8080`
      in `/etc/pacman/env` and `systemctl restart pacman`. Firewall: allow 80/443 only.
- [ ] **Log in from each machine:** `pacman login https://<domain> <token>`.
      Machines that already have a config for the VM just need this one command.
- [ ] Update `README.md` with the domain once it exists.
- [x] systemd unit and one-command install (`pacman serve -install`).

## Token handling

- **Where it lives on the server:** `/etc/pacman/env`, mode 600, root-owned. The
  installer writes it; the unit loads it with `EnvironmentFile`, so it never shows
  in `ps`. Nobody but root can read it.
- **Choosing your own instead of a generated one:** `sudo PACMAN_TOKEN=<t> ./pacman serve -install`
  keeps it out of shell history. `-token <t>` works too but lands in history.
- **On clients:** `pacman login URL TOKEN` stores it in `~/.config/pacman/config`
  (mode 600) and sends it as a Bearer header. Use `?token=` in URLs only for the
  one-off curl that bootstraps a fresh machine, since URLs end up in shell history
  and proxy logs.
- **Rotating it:** `sudo PACMAN_TOKEN=<new> pacman serve -install` on the server,
  then `pacman login URL <new>` on each client. There is one shared token, so every
  client changes at once.
- **Upgrading the server:** copy the new binary over and run
  `sudo ./pacman serve -install` again. Token and address are kept.
