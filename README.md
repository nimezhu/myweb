# myweb

A small local web server for a folder of data files. It serves the folder over a
JSON/HTTP API (read, list, write, upload, move, delete, search, live change
events) so web apps can use it as their storage. It comes with a browser file
manager and an interactive terminal UI.

- **Safe by default:** listens on `127.0.0.1` only; overwritten files keep their
  previous versions and deleted files go to a trash; writes can be conditional so
  two apps never silently overwrite each other.
- **One binary:** the file manager is embedded; no runtime dependencies.
- **A library too:** the server, terminal UI and file manager are Go packages,
  used by [cellome](https://github.com/mycellome/cellome) as well.

## Quick start

```sh
go build -o myweb .
./myweb -d ~/data          # serve ~/data; opens the file manager in your browser
```

The terminal shows the address, the request log and the keys. Without a
terminal (or with `-no-tui`) it prints plain log lines instead.

## Options

| Flag | Default | |
|---|---|---|
| `-d DIR` | `.` | folder to serve |
| `-p PORT` | `8433` | port; the next free one is used if it is taken |
| `-web DIR` | | serve your own web app at `/` (the file manager moves to `/_myweb/`) |
| `-web-from URL` | | install the app at `/` from a signed manifest and keep it updated (see below) |
| `-web-key KEY` | | public key that signs the `-web-from` manifest (required with it) |
| `-web-version V` | | serve this installed version and don't update (rollback) |
| `-no-update` | | with `-web-from`: serve what is installed, don't check |
| `-web-check DUR` | `6h` | with `-web-from`: how often to check for updates |
| `-no-manager` | | don't serve the built-in file manager |
| `-r` | | read-only (toggle with `r` in the terminal UI) |
| `-global` | | listen on all interfaces, i.e. visible to your network |
| `-auth` | | require an access token for writes and commands |
| `-token T` | `$MYWEB_TOKEN` | the token to use (implies `-auth`); otherwise a random one per start |
| `-cors ORIGIN` | `$MYWEB_CORS` | extra browser origins allowed to call the API; repeat or comma-separate |
| `-versions N` | `20` | previous versions kept per file on overwrite (`0`: off) |
| `-trash-days N` | `30` | days deleted files stay in the trash (`0`: delete immediately) |
| `-upload-max MIB` | `100` | largest file accepted by an upload |
| `-log FILE` | | also append requests and messages to FILE as JSON lines (rotates at 50 MB) |
| `-no-tui` | | plain log output |
| `-no-open` | | don't open the browser on start |
| `-v`, `-version` | | print the version |

## Terminal UI

| Key | |
|---|---|
| `o` | open the browser (signed in when auth is on) |
| `m` | open the file manager (when `-web` serves an app at `/`) |
| `a` | check for an app update now (`-web-from`) |
| `r` | toggle read-only |
| `/` | filter the request log; `e` shows errors only |
| `↑` `↓` | select a request; `⏎` shows its details (including the error response) |
| `u` | QR code of the network address, to open it on a phone (`-global`) |
| `c` | clear the log |
| `esc` | step back (details → selection → filter) |
| `q`, `ctrl+c` | quit |

## File manager

At `/` (or `/_myweb/` when `-web` is set): browse folders, preview files
(images, video, audio, PDF, Markdown, CSV/TSV, JSON, text; HTML is shown as
source and never run), search names and contents, upload files and whole
folders by dropping them on the list, rename, delete, browse and restore
previous versions, and restore files from the trash. It updates live as files
change, and lists and runs the plugin commands.

## Install the app from a distribution server

Instead of `-web DIR`, myweb can install your web app from a server you publish
to, and keep it up to date:

```sh
# once: make a signing key (keep app.key secret, e.g. in CI secrets)
go run github.com/nimezhu/myweb/cmd/myweb-publish keygen -out app.key

# each release: zip the built app and write the signed manifest
myweb-publish build -key app.key -name myapp -version 1.4.2 -dir ./dist -out ./release
myweb-publish verify -pub <public key> -manifest release/stable.json
# upload release/ (stable.json, stable.json.sig, myapp-1.4.2.zip) to any web server

# users
myweb -d ~/data -web-from https://dist.example.com/myapp/stable.json -web-key <public key>
```

- **First start** shows an "installing…" page that turns into the app as soon as
  it is ready. **Later starts** serve the installed version immediately and check
  for updates in the background (`-web-check`, or `a` in the terminal UI).
- **Updates** are whole versions, switched in one step. Open pages receive
  `{"type":"app-updated","name":…,"version":…}` on `/events` and can offer a reload.
  The newest 3 versions are kept; `-web-version` serves an older one.
- **Safety:** the app runs with full access to your files, so myweb installs it
  only if the manifest's ed25519 signature matches `-web-key` and the zip matches
  the signed SHA-256. There is no switch to skip this. It never moves to an older
  version by itself, refuses zips with paths that leave the app folder, accepts
  only https (http just for localhost), and keeps serving the installed version
  when the server can't be reached. A manifest can require a myweb version with
  `-requires-myweb`.
- Apps are installed in the user's cache folder, never in the data folder:
  `%LocalAppData%\myweb\apps` on Windows, `~/Library/Caches/myweb/apps` on macOS,
  `~/.cache/myweb/apps` (or `$XDG_CACHE_HOME/myweb/apps`) on Linux. Don't edit
  files there; they are replaced on update. The file manager stays at `/_myweb/`.

## HTTP API

Paths below are relative to the served folder. Errors are JSON:
`{"success":0,"error":"…"}` with a matching HTTP status.

### Files

| | |
|---|---|
| `GET /f/<path>` | the file (Range requests, `ETag`, `Cache-Control: no-cache`) |
| `GET /ls/<dir>` | `[{name, size, mode, modtime, isdir, issymlink}]` |
| `POST /w/<path>` | write the request body to the file (creates folders); `{success, file:{url, name, size, etag}}` |
| `POST /rm/<path>` | delete a file (into the trash) or an empty folder |
| `POST /up/<dir>` | multipart upload of any number of files; file names may contain sub-folders; `?onconflict=rename` (default, `a (1).txt`) \| `overwrite` \| `fail` |
| `POST /cmd/uploadFile` | one file (field `myFile`) into `uploads/<md5>_<name>` (for editor.js) |

**Conditional writes.** `/w/` and `/rm/` honour `If-Match: <ETag>` (only if the
file is unchanged since it was read) and `If-None-Match: *` (only if it does not
exist yet). Otherwise they answer `412` with the current `ETag`, and nothing is
changed.

### Commands

`POST /cmd/<name>` with a JSON body returns `{"success":1,"result":…}`;
`GET /cmd/` lists them.

| Command | Params | |
|---|---|---|
| `stat` | `{path}` | exists, size, modification time |
| `mkdir` | `{path}` | create a folder and its parents |
| `mv` | `{from, to, overwrite}` | move or rename a file or folder |
| `find` | `{dir, pattern, limit}` | files whose name matches a glob |
| `grep` | `{pattern, dir, glob, regex, ignoreCase, limit}` | lines in text files that contain the pattern |
| `history` | `{path}` | previous versions of a file, newest first |
| `restore` | `{path, version}` | make a version current (the current content is kept as a version) |
| `trash` | | deleted files |
| `untrash` | `{id, overwrite}` | put a deleted file back |
| `emptyTrash` | | delete the trash permanently |

Also: `GET /cmd/fetchUrl?url=` (a page's OpenGraph metadata), `GET /cmd/status`
(version, read-only and sign-in state), `POST /cmd/stopServer` (same-origin
only), `GET /version`.

### Live changes: `/events` (WebSocket)

```js
const ws = new WebSocket("ws://127.0.0.1:8433/events")
ws.onopen = () => ws.send(JSON.stringify({ op: "watch", path: "/notes" }))   // a folder, a file, or {recursive: true}
ws.onmessage = (e) => console.log(JSON.parse(e.data))
// {"type":"change","path":"/notes/a.md","exists":true,"isdir":false}
// {"type":"status","readOnly":true}
```

Only watched paths are watched on disk. Send `{"op":"unwatch","path":…}` to stop,
and `{"op":"auth","token":…}` when a token is needed (browsers can't set headers
on a WebSocket).

### Versions and trash

They are kept in `.myweb/` inside the served folder: `.myweb/history/<path>/<version>`
and `.myweb/trash/<id>/<path>`. Clients can read them through `/f/.myweb/…` but
not write them; `.myweb` is hidden from listings, searches and events. A version
is a hard link to the replaced file, so it costs no space until the file changes.

## Access and security

- **Local by default.** Only `127.0.0.1` can connect unless `-global` is given.
- **Auth** (`-auth`, `-token`, `MYWEB_TOKEN`) protects writes, uploads and
  commands, and with `-global` also reads. Scripts send
  `Authorization: Bearer <token>`. Browsers sign in through the link the server
  opens (`/?token=…`), which sets a cookie for this server only.
- **CORS.** Browsers may call the API only from the server's own pages, from
  localhost, from `https://mycellome.com`, `https://youdata.studio` and
  `https://cellcycle.org` and their subdomains (`myserver.DefaultCORS`), plus any
  given with `-cors`. A bare `*` is refused. The same list is checked
  for `/events`.
- **Paths** cannot leave the served folder, and `.myweb` cannot be written.

## Use it from Go

```go
import (
	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/manager"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/plugins"
)

con := console.New(console.Options{Name: "myapp"})
srv := myserver.New(myserver.Config{
	Root:      "/data",
	Manager:   manager.FS(),
	CORS:      myserver.DefaultCORS,
	OnRequest: con.OnRequest,
})
plugins.RegisterFileOps(srv) // stat, mkdir, mv, find, grep

srv.RegisterCmd("hello", func(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
	var p struct{ Name string }
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, myserver.Errorf(http.StatusBadRequest, "name is required")
	}
	return "hello " + p.Name, nil // → {"success":1,"result":"hello …"}
}, myserver.Describe(`say hello: {"name":"you"}`))

if err := srv.Start(); err != nil {
	console.Fatal("myapp", err)
}
os.Exit(con.Run(srv)) // terminal UI until quit, then a graceful shutdown
```

Command options: `myserver.Public()` (no token needed), `myserver.Writes()`
(refused in read-only mode), `myserver.Describe(…)`. Use `srv.ResolvePath(p)` for
any path a command touches, and `srv.KeepVersion(p)` before overwriting a file.

From JavaScript, [`@reflexiv/storage`](https://github.com/reflexdata/storage)'s
`myome` store wraps this API: keys and values, conditional saves, `watch()`,
`history()` and `restore()`.

## Development

```sh
go test ./...           # add -race to check for data races
go vet ./...
```
