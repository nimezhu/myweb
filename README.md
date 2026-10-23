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
./myweb                    # serve ~/.myweb/data (created on first start)
./myweb -d ~/data          # serve another folder; -d . for the current one
```

The terminal shows the address, the request log and the keys. Without a
terminal (or with `-no-tui`) it prints plain log lines instead.

## Commands

`myweb` with only flags serves files, as above (`myweb serve` is the same).
The other commands manage installed web apps (see
[below](#install-the-app-from-a-distribution-server)):

| Command | |
|---|---|
| `myweb [serve] [flags]` | serve files; `myweb serve -h` lists the flags |
| `myweb app [list]` | list the installed apps |
| `myweb app run NAME [flags]` | serve an installed app (same as `-app NAME`) |
| `myweb app install -from URL -key KEY` | install an app, or update it, without serving it |
| `myweb app update [NAME]` | update one or all installed apps |
| `myweb app versions NAME` | the installed versions; `*` marks the one served |
| `myweb app remove NAME [-y]` | delete an installed app (asks first) |
| `myweb app default [NAME \| -clear]` | the app that plain `myweb` serves |
| `myweb config` | show the saved defaults for the flags |
| `myweb config set NAME VALUE` / `unset NAME` | e.g. `config set p 9000`, `config set d ~/data` |
| `myweb version`, `myweb help` | |

All of them take `-home DIR`.

**Defaults:** `config set` saves a flag's value in `~/.myweb/config.json`, so
plain `myweb` uses it; a flag given on the command line wins. With a default
app (`myweb app default myapp`), `myweb` alone serves that app; `-web`,
`-web-from` or `-app` on the command line replace it, and `-app ""` serves
just the files.

**One copy:** starting myweb again for the same folder and app opens the copy
that is already running instead of starting a second one on another port.
`-new` starts another copy anyway.

## Options

| Flag | Default | |
|---|---|---|
| `-home DIR` | `$MYWEB_HOME`, else `~/.myweb` | myweb's own folder: the default data folder (`data/`) and installed apps (`apps/`) |
| `-d DIR` | `~/.myweb/data` | folder to serve (created if it's the default); `-d .` for the current folder |
| `-p PORT` | `8433` | port; the next free one is used if it is taken |
| `-web DIR` | | serve your own web app at `/` (the file manager moves to `/_myweb/`) |
| `-app NAME` | | serve an app installed with `-web-from`, by name, and keep it updated |
| `-apps` | | list the installed apps and exit |
| `-web-from URL` | | install the app at `/` from a signed manifest and keep it updated (see below) |
| `-web-key KEY` | | public key (or a file holding it) that signs the `-web-from` manifest (required with it) |
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
| `-new` | | start another copy even if one already serves this folder and app |
| `-v`, `-version` | | print the version |

## Terminal UI

| Key | |
|---|---|
| `o` | open the browser (signed in when auth is on) |
| `y` | copy the startup URL (includes the login token when auth is on) |
| `m` | open the file manager (when `-web` serves an app at `/`) |
| `a` | check for an app update now (`-web-from`) |
| `r` | toggle read-only |
| `/` | filter the request log; `e` shows errors only |
| `↑` `↓` | select a request; `⏎` shows its details (including the error response) |
| `u` | QR code of the network address, to open it on a phone (`-global`) |
| `c` | clear the log |
| `esc` | step back (details → selection → filter) |
| `q`, `ctrl+c` | quit |

Copying uses the terminal's clipboard support (OSC 52), including over SSH; the terminal must allow clipboard
writes. If it does not, `myweb -no-tui -no-open` prints the startup URL for manual copying.

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
# once: make a signing key: app.key is secret (keep it e.g. in CI secrets),
# app.key.pub is the public key you give to users
go run github.com/nimezhu/myweb/cmd/myweb-publish keygen -out app.key
# lost app.key.pub? myweb-publish pubkey -key app.key prints it again

# each release: zip the built app and write the signed manifest
myweb-publish build -key app.key -name myapp -version 1.4.2 -dir ./dist -out ./release
myweb-publish verify -pub app.key.pub -manifest release/stable.json   # build checks this too
# upload release/ (stable.json, stable.json.sig, myapp-1.4.2.zip) to any web server

# users
myweb -d ~/data -web-from https://dist.example.com/myapp/stable.json -web-key <public key, or app.key.pub>
# after the first install, by name (myweb remembers the URL and the key)
myweb app run myapp -d ~/data   # or: myweb -d ~/data -app myapp
myweb app list                  # what is installed
myweb app update                # update everything now, without serving
```

- **First start** shows an "installing…" page that turns into the app as soon as
  it is ready. **Later starts** serve the installed version immediately and check
  for updates in the background (`-web-check`, or `a` in the terminal UI).
- **Updates** are whole versions, switched in one step. The newest 3 versions
  are kept; `-web-version` serves an older one.
- **Reload prompt:** add `<script src="/_myweb/client.js"></script>` to the app
  and open pages offer "myapp 1.4.3 is ready. Reload" with the release notes when
  a new version is installed. For your own prompt, use
  `data-banner="off"` and listen for the `myweb:app-updated` event on `window`
  (`e.detail` is `{name, version, notes}`). Without the script, pages can watch
  `/events` for `{"type":"app-updated","name":…,"version":…,"notes":…}`.
- **Release notes** (`build -notes`) are shown by `myweb app update`,
  `myweb app versions`, the file manager and the reload prompt.
- **In the browser,** the file manager (`/_myweb/`) shows the app: version,
  status, last check, notes, installed versions and a "Check for updates" button.
  Pages and tools get the same as JSON from `POST /cmd/app` (`{"check":true}`
  starts a check; admins only with myweb-team).
- **Safety:** the app runs with full access to your files, so myweb installs it
  only if the manifest's ed25519 signature matches `-web-key` and the zip matches
  the signed SHA-256. There is no switch to skip this. It never moves to an older
  version by itself, refuses zips with paths that leave the app folder, accepts
  only https (http just for localhost), and keeps serving the installed version
  when the server can't be reached. A manifest can require a myweb version with
  `-requires-myweb`.
- Apps are installed in `~/.myweb/apps` (`C:\Users\<you>\.myweb\apps` on
  Windows), never in the data folder. Don't edit files there; they are replaced
  on update. The file manager stays at `/_myweb/`.

## Several people: myweb-team

`myweb-team` is myweb for a team or lab: everyone signs in with a personal link,
has a private folder, and folders can be shared.

```sh
go build -o myweb-team ./cmd/myweb-team

myweb-team user add ann -role admin -url http://lab-server:8433   # prints ann's sign-in link
myweb-team user add bob                                           # member (default)
myweb-team user add vic -role viewer
myweb-team share /projects/x -read bob,vic -write ann             # * = everyone signed in
myweb-team user list ; myweb-team folders
myweb-team serve -global                                          # same flags as myweb
```

- **Roles:** `admin` may do everything; `member` has `/users/<name>` plus the
  shared folders they are listed on; `viewer` is like member but never writes.
- **Folder rules** apply to a folder and everything in it; the most specific rule
  decides; writers can also read.
- **Everything enforces it:** files, listings, the file manager, uploads, `mv`,
  search (`find`, `grep`), live events, versions and the trash (which records who
  deleted what). Files you may not see answer 404, so their names don't leak.
- **Sign-in links** carry a personal token; only its SHA-256 is stored, in
  `~/.myweb-team/team.json` (`$MYWEB_TEAM_HOME`, or `-home DIR` on every
  command). `user reset` replaces a link,
  `user remove` revokes it. The running server picks up changes within a second,
  also for connections already open. Requests are logged with the user's name.
- **HTTPS:** over a network, links and cookies travel in clear text unless HTTPS
  is in front, e.g. [Caddy](https://caddyserver.com) as a reverse proxy, or a
  private network such as Tailscale.

## Folders

Everything myweb keeps lives in `~/.myweb` (`C:\Users\<you>\.myweb` on Windows):
`data/` is the default data folder, `apps/` holds web apps installed with
`-web-from`, `config.json` the saved defaults and `run/` a note per running copy. Start with `-home DIR`, or set `MYWEB_HOME`, to keep it elsewhere (e.g. on another disk or a
USB stick). Inside every served folder, versions and the trash are kept in its
own `.myweb/` subfolder.

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
(version, read-only and sign-in state), `POST /cmd/app` (the installed web app,
with `-web-from` or `-app`), `POST /cmd/stopServer` (same-origin
only), `GET /version`.

### CORS proxy: `/proxy?url=`

`GET` (or `HEAD`) `/proxy?url=<http or https URL>` fetches the URL and streams the answer back
with this server's CORS headers, so a web app can read data from sites that don't allow it
directly:

```js
const r = await fetch("http://127.0.0.1:8433/proxy?url=" + encodeURIComponent(remote),
                      { headers: { Range: "bytes=0-65535" } })
```

- `Range`, `If-Range`, `If-None-Match`, `If-Modified-Since`, `Accept` and `Accept-Language`
  are forwarded, so partial reads of large files work. The browser's cookies and token are not.
- Status, `Content-Type`, `Content-Length`, `Content-Range`, `Accept-Ranges`, `ETag`,
  `Last-Modified` and cache headers come back; `X-Proxy-Final-URL` is the URL after redirects.
- Only public addresses are reachable: not this machine, the local network, or link-local
  (cloud metadata) addresses, even through a redirect. The answer is `403`.
- Needs the token when auth is on, like writes. Responses are sandboxed
  (`Content-Security-Policy: sandbox`), so a proxied page can't run scripts on this server's origin.
- `HTTP_PROXY`/`HTTPS_PROXY` are not used.

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

For several users, set `myserver.Config.Access` (see `myserver/access.go`; the
`team` package is one implementation); commands then get `req.User`, and should
resolve every path with `req.Path(myserver.ActRead|ActWrite, p)`. The `cli`
package is the whole command line (`cli.Serve`), used by both programs.

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
