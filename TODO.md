# TODO: put pacman on prod

Right now pacman only runs by hand on the local `pacman` VM (192.168.122.71,
token `devtoken`). Nothing below is done yet.

- [ ] **Pick and register the new domain** for this service and point an A (and AAAA)
      record at the prod box.
- [ ] **Decide the ssh alias / user for the prod box** so `make deploy` can target it.
- [ ] **TLS in front.** Plain HTTP on the public internet leaks the token on the wire.
      Preferred: Caddy on 80/443 reverse-proxying to pacman on `127.0.0.1:8080`
      (automatic certificates). nginx + certbot works too.
- [ ] **systemd unit + `make deploy`** (cross-build static binary, scp to
      `/usr/local/bin/pacman.new`, atomic `mv`, `systemctl restart pacman`).
      Run as a dedicated `pacman` user, data in `/var/lib/pacman`.
- [ ] **Generate and install the real token** (steps below). Never reuse `devtoken`.
- [ ] **Firewall:** allow 80/443 only. Keep pacman bound to `127.0.0.1:8080`
      (`-addr 127.0.0.1:8080`) so it is reachable only through the proxy.
- [ ] Update `README.md` with the domain and the deploy command once it exists.

## Getting the token onto prod

The token is a shared secret. It must never land in shell history, a commit,
or a log line on either side.

1. **Generate it on your machine** (64 hex chars is plenty):

   ```sh
   openssl rand -hex 32
   ```

   Store it in your password manager immediately. That is the copy of record.

2. **Put it on the box without echoing it in a command line.** Write it via
   stdin so it never appears in `ps` or `~/.bash_history` on prod:

   ```sh
   # locally: paste the token once when prompted, then Ctrl-D
   ssh root@<prod> 'umask 077 && mkdir -p /etc/pacman && cat > /etc/pacman/env'
   ```

   Type the line `PACMAN_TOKEN=<paste>` and finish with Ctrl-D.

   Or from a local file that you delete afterwards:

   ```sh
   printf 'PACMAN_TOKEN=%s\n' "$(openssl rand -hex 32)" > pacman.env
   chmod 600 pacman.env
   scp pacman.env root@<prod>:/etc/pacman/env
   ssh root@<prod> 'chown root:pacman /etc/pacman/env && chmod 640 /etc/pacman/env'
   shred -u pacman.env   # keep the value only in the password manager
   ```

3. **The systemd unit reads it**, so the token is never on the command line:

   ```ini
   [Service]
   EnvironmentFile=/etc/pacman/env
   ExecStart=/usr/local/bin/pacman -addr 127.0.0.1:8080 -dir /var/lib/pacman
   User=pacman
   ```

4. **On clients, prefer the header over the query string.** `?token=` ends up in
   the client's shell history and in any proxy access log. Keep it in a file:

   ```sh
   mkdir -p ~/.config/pacman && chmod 700 ~/.config/pacman
   printf '%s' '<token>' > ~/.config/pacman/token && chmod 600 ~/.config/pacman/token

   # then
   curl -H "Authorization: Bearer $(cat ~/.config/pacman/token)" https://<domain>/files
   ```

   Use `?token=` only for one-off pulls on a throwaway machine (`wget`, cloud-init, etc).

5. **Rotating it** is: generate a new one, replace the line in `/etc/pacman/env`,
   `systemctl restart pacman`, update the password manager and client files.
   There is only one token, so every client changes at once.
