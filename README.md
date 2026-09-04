# pacman

A single static Go binary that stores files you upload and hands them back over
plain HTTP. Every request needs a shared token, so you can drop a build on a box,
ask it for the list of paths, and pull them down from any other machine.

No dependencies beyond the Go standard library.

## Build

```sh
make static        # CGO_ENABLED=0, stripped, runs on any x86-64 Linux
# or: CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o pacman .
```

## Run

```sh
PACMAN_TOKEN=changeme ./pacman -addr :8080 -dir /var/lib/pacman
```

| flag     | env            | default  |                                    |
|----------|----------------|----------|------------------------------------|
| `-addr`  | `PACMAN_ADDR`  | `:8080`  | listen address                     |
| `-dir`   | `PACMAN_DIR`   | `./data` | storage directory, created if missing |
| `-token` | `PACMAN_TOKEN` | required | prefer the env var so it stays out of `ps` |

## API

Send the token as `Authorization: Bearer <token>` **or** `?token=<token>`.
Names are flat: letters, digits, `.`, `_`, `-`, not starting with `.`.

| Method       | Path            | Result                                                        |
|--------------|-----------------|---------------------------------------------------------------|
| `GET`        | `/files`        | `{"files":[{name,size,modified,path,url}, ...]}`              |
| `PUT`/`POST` | `/files/<name>` | body = file bytes. `201` created, `200` overwritten, `400` empty |
| `GET`/`HEAD` | `/files/<name>` | the bytes, with `Range` support for resumable downloads       |
| `DELETE`     | `/files/<name>` | `204`                                                         |
| `GET`        | `/healthz`      | `ok` (the only route without auth)                            |

Uploads go to a temp file in the storage dir and are renamed into place, so a
download never sees a half-written file. The token is redacted from the request log.

## Examples

```sh
H='Authorization: Bearer changeme'
curl -T ./mytool -H "$H" http://box:8080/files/mytool        # upload
curl -H "$H" http://box:8080/files                            # list
curl -o mytool 'http://box:8080/files/mytool?token=changeme'  # download from anywhere
wget 'http://box:8080/files/mytool?token=changeme' -O mytool  # same, with wget
curl -X DELETE -H "$H" http://box:8080/files/mytool           # delete
```

## Test

```sh
make test
```

## Trying it on a fresh VM (virtman)

```sh
make static
virtman make alma10 --name pacman
scp ./pacman pacman:~/pacman
ssh pacman 'sudo firewall-cmd --add-port=8080/tcp'
ssh pacman 'PACMAN_TOKEN=devtoken ~/pacman -addr :8080 -dir ~/pacman-data'
```

Then from the host or any other VM on the same network:

```sh
curl -T somefile 'http://<vm-ip>:8080/files/somefile?token=devtoken'
curl 'http://<vm-ip>:8080/files?token=devtoken'
curl -o somefile 'http://<vm-ip>:8080/files/somefile?token=devtoken'
```
