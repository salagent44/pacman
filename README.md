# pacman

A single static Go binary that is both a private file drop and its own client.
Run `pacman serve` on one box, upload your binaries to it, and every other
machine you own can list, download and keep them up to date with one command.
Every request needs a shared token.

No dependencies beyond the Go standard library. The binary carries no server
address or token; those live in a config file on each machine.

## Build

```sh
make static        # CGO_ENABLED=0, stripped, version stamped, runs on any x86-64 Linux
```

## Server

### Install on a VPS (two commands, one of them from your laptop)

```sh
scp ./pacman vps:                          # from your machine
ssh vps 'sudo ./pacman serve -install'     # on the box
```

`-install` needs root and systemd. It copies the binary to `/usr/local/bin/pacman`,
generates a token (or takes `-token T` / `PACMAN_TOKEN`) and writes it to
`/etc/pacman/env` (mode 600), writes `pacman.service` with `DynamicUser` and a
state directory at `/var/lib/pacman`, enables and starts it, waits for `/healthz`,
and uploads the binary into the drop so every other machine can bootstrap from it.
The token is printed exactly once; put it in your password manager.

Run the same command again with a newer binary to upgrade. The existing token and
listen address are kept unless you pass `-token` or `-addr`. Logs are in
`journalctl -u pacman -f`. Tested on AlmaLinux 10 with SELinux enforcing.

Before exposing it to the internet put TLS in front, for example Caddy:

```
your.domain { reverse_proxy 127.0.0.1:8080 }
```

then set `PACMAN_ADDR=127.0.0.1:8080` in `/etc/pacman/env` and `systemctl restart pacman`.

### Run by hand

```sh
PACMAN_TOKEN=changeme ./pacman serve -addr :8080 -dir /var/lib/pacman
```

| flag     | env            | default  |                                            |
|----------|----------------|----------|--------------------------------------------|
| `-addr`  | `PACMAN_ADDR`  | `:8080`  | listen address                             |
| `-dir`   | `PACMAN_DIR`   | `./data` | storage directory, created if missing      |
| `-token` | `PACMAN_TOKEN` | required | prefer the env var so it stays out of `ps` |

Names are flat: letters, digits, `.`, `_`, `-`, not starting with `.`. Uploads go
to a temp file in the storage dir and are renamed into place, so a download never
sees a half-written file. The token is redacted from the request log.

## Client

Bootstrap a fresh machine with nothing but curl, because the client is itself
one of the files on the server:

```sh
curl -OJ 'http://box:8080/files/pacman?token=changeme' && chmod +x pacman
./pacman login http://box:8080 changeme        # writes ~/.config/pacman/config (mode 600)
./pacman install pacman mytool othertool       # into ~/.local/bin, chmod +x, remembered
```

From then on:

```sh
pacman ls                       # what the server holds, and what you have installed
pacman update                   # re-download anything installed that changed, itself included
pacman put ./build/mytool       # upload (name defaults to the basename)
pacman get mytool /tmp          # plain download, no install bookkeeping
pacman rm mytool                # delete from the server
pacman version
```

| command                          | what it does                                                        |
|----------------------------------|---------------------------------------------------------------------|
| `login URL TOKEN`                | save server and token to `~/.config/pacman/config`                  |
| `ls`                             | list files with size, modified time, install path, URL              |
| `put FILE [NAME]`                | upload                                                              |
| `get NAME [DEST]`                | download to `DEST` (default `./NAME`; a directory is fine)          |
| `install [-dir D] NAME...`       | download to `~/.local/bin` (or `D` / `$PACMAN_BIN`), chmod +x, track |
| `update [NAME...]`               | re-fetch tracked files whose size or mtime changed on the server     |
| `rm NAME`                        | delete on the server                                                |

`PACMAN_URL` and `PACMAN_TOKEN` override the config file, so scripts and
cloud-init never need `login`. Tracked installs live in
`~/.local/share/pacman/installed.json`. Downloads are written to a temp file
beside the target and renamed into place, which is also how `pacman update`
safely replaces its own running executable on Linux.

## HTTP API

Send the token as `Authorization: Bearer <token>` **or** `?token=<token>`.

| Method       | Path            | Result                                                          |
|--------------|-----------------|-----------------------------------------------------------------|
| `GET`        | `/files`        | `{"files":[{name,size,modified,path,url}, ...]}`                |
| `PUT`/`POST` | `/files/<name>` | body = file bytes. `201` created, `200` overwritten, `400` empty |
| `GET`/`HEAD` | `/files/<name>` | the bytes, with `Range` support for resumable downloads         |
| `DELETE`     | `/files/<name>` | `204`                                                           |
| `GET`        | `/healthz`      | `ok` (the only route without auth)                              |

Plain curl still works everywhere:

```sh
H='Authorization: Bearer changeme'
curl -T ./mytool -H "$H" http://box:8080/files/mytool
curl -H "$H" http://box:8080/files
curl -OJ 'http://box:8080/files/mytool?token=changeme'   # -J names the file from the server
```

## Test

```sh
make test
```

## Trying it on a fresh VM (virtman)

```sh
make static
virtman make alma10 --name pacman
scp ./pacman pacman:
ssh pacman 'sudo ./pacman serve -install'      # prints the token once
```

Then bootstrap any other VM from it:

```sh
ssh other-vm "curl -OJ 'http://<vm-ip>:8080/files/pacman?token=<token>' && chmod +x pacman \
  && ./pacman login http://<vm-ip>:8080 <token> && ./pacman install pacman && pacman ls"
```
