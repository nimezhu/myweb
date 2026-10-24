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

Download the binary for your system from the
[releases](https://github.com/nimezhu/myweb/releases), or build it:

```sh
go install github.com/nimezhu/myweb@latest   # or, in a checkout:
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
| `myweb app catalog [WORD]` | the apps you can install from the catalogs (see [below](#app-catalogs-install-by-name)) |
| `myweb app install NAME` | install an app from a catalog (`app run NAME` installs it too) |
| `myweb app install -from URL -key KEY` | install any app, or update it, without serving it |
| `myweb app update [NAME]` | update one or all installed apps |
| `myweb app versions NAME [-all]` | the installed versions, or with `-all` every published one; `*` marks the one served |
| `myweb app use NAME VERSION` | serve that version (installing it if needed) and stop updating; `latest` follows updates again |
| `myweb app remove NAME [-y]` | delete an installed app (asks first) |
| `myweb app default [NAME \| -clear]` | the app that plain `myweb` serves |
| `myweb status [-json]` | the servers running now, numbered |
| `myweb open [WHICH]` | open one in the browser, signed in |
| `myweb stop [WHICH \| -all]` | stop one, or all; `WHICH` is the number from `status`, a pid, a port, an app's name or a data folder, and can be left out when only one runs |
| `myweb service install [-as NAME] [flags]` | run in the background, started at login (see below) |
| `myweb service uninstall`, `status` | |
| `myweb config` | show the saved defaults for the flags |
| `myweb config set NAME VALUE` / `unset NAME` | e.g. `config set p 9000`, `config set d ~/data` |
| `myweb upgrade [VERSION]` | install the latest release from GitHub, or that version; `-check` only tells, `-rollback` goes back |
| `myweb completion bash\|zsh\|fish` | shell completion of commands, flags, app names and versions |
| `myweb version`, `myweb help` | |

All of them take `-home DIR`.

**Completion:** add `source <(myweb completion bash)` to `~/.bashrc`
(`zsh` likewise, after `compinit`; fish:
`myweb completion fish > ~/.config/fish/completions/myweb.fish`).

**Defaults:** `config set` saves a flag's value in `~/.myweb/config.json`, so
plain `myweb` uses it; a flag given on the command line wins. With a default
app (`myweb app default myapp`), `myweb` alone serves that app; `-web`,
`-web-from` or `-app` on the command line replace it, and `-app ""` serves
just the files.

**One copy:** starting myweb again for the same folder and app opens the copy
that is already running instead of starting a second one on another port.
`-new` starts another copy anyway.

**In the background:** `myweb service install -app myapp -d ~/data` runs
myweb with those flags now and at every login, without a terminal: a systemd
user service on Linux (logs: `journalctl --user -u myweb`; it runs while you
are logged in unless `loginctl enable-linger`), a LaunchAgent on macOS and a
hidden launcher in the Startup folder on Windows (logs in `~/.myweb/logs`). No
admin rights are needed. Running `myweb` for the same folder and app then just
opens it. `-as NAME` installs a second one (e.g. one per app), `-dry-run`
shows what would be written, and installing again replaces the flags. Keep a
token in `myweb config set token …`, not in the service's flags.

**Upgrading:** `myweb upgrade` downloads the latest
[release](https://github.com/nimezhu/myweb/releases) for your system, checks
it against the release's `checksums.txt` and that it runs, and replaces the
binary, keeping the old one next to it as `myweb.old` (`myweb upgrade
-rollback` swaps them back). Servers already running keep the old version
until restarted. A running server tells, at most once a day, when a newer
release is out; `-no-upgrade-check` (or `myweb config set no-upgrade-check
true`) turns that off. A binary in a folder you can't write to (e.g.
`/usr/local/bin`) is upgraded with the rights to write there.

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
| `-no-upgrade-check` | | don't look for a newer myweb release (checked once a day) |
| `-v`, `-version` | | print the version |

## Terminal UI

| Key | |
|---|---|
| `o` | open the browser (signed in when auth is on) |
| `y` | copy the startup URL (includes the login token when auth is on) |
| `m` | open the file manager (when `-web` serves an app at `/`) |
| `a` | the app view (`-web-from`, `-app`): status, release notes and versions; in it `⏎` serves the selected version, `l` follows updates again, `c` checks now, `p` lists every published version, `esc` goes back |
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
previous versions, and restore files from the trash. Download a folder, or the
files and folders you tick, as one `.zip`, and extract a `.zip` into a new
folder next to it. It updates live as files
change, and lists and runs the plugin commands.

## Install the app from a distribution server

Instead of `-web DIR`, myweb can install your web app from a server you publish
to, and keep it up to date:

```sh
# once, in your app's project: settings in myweb-publish.json, and a signing
# key: app.key is secret (keep it out of git, e.g. in CI secrets), app.key.pub
# is the public key you give to users
go install github.com/nimezhu/myweb/cmd/myweb-publish@latest
myweb-publish init -name myapp -dir dist          # -out release -key app.key -keep N

# each release: zip dist/, sign the manifest and the list of every version
myweb-publish build -version 1.0.0                # the first one
myweb-publish build -bump patch -notes "Fix the export"   # then: patch, minor or major
# the folders of the user's data folder the app uses: -folder /myapp (repeat;
# :ro to only read), or "folders": ["/myapp"] in myweb-publish.json
myweb-publish versions                            # what release/ publishes
# upload release/ as is (stable.json, stable.versions.json, their .sig, the zips)
# to any web server; build checks it like myweb will (myweb-publish verify too)

# users
myweb -d ~/data -web-from https://dist.example.com/myapp/stable.json -web-key <public key, or app.key.pub>
# after the first install, by name (myweb remembers the URL and the key)
myweb app run myapp -d ~/data   # or: myweb -d ~/data -app myapp
myweb app list                  # what is installed
myweb app update                # update everything now, without serving
myweb app use myapp 1.3.0       # go back to 1.3.0 and stay there
myweb app use myapp latest      # follow updates again
```

- **First start** shows an "installing…" page that turns into the app as soon as
  it is ready. **Later starts** serve the installed version immediately and check
  for updates in the background (`-web-check`, or `a` in the terminal UI).
- **Updates** are whole versions, switched in one step. The newest 3 versions
  are kept.
- **Any version:** `myweb app use myapp 1.3.0` (or "use" in the file manager)
  installs any version the publisher lists, verified like an update, and serves
  it from then on without updating; a running myweb switches right away.
  `latest` follows updates again. `-web-version` serves an installed version
  for one run only.
- **Reload prompt:** add `<script src="/_myweb/client.js"></script>` to the app
  and open pages offer "myapp 1.4.3 is ready. Reload" with the release notes when
  a new version is installed. For your own prompt, use
  `data-banner="off"` and listen for the `myweb:app-updated` event on `window`
  (`e.detail` is `{name, version, notes}`). Without the script, pages can watch
  `/events` for `{"type":"app-updated","name":…,"version":…,"notes":…}`.
- **Release notes** (`build -notes`) are shown by `myweb app update`,
  `myweb app versions`, the file manager and the reload prompt.
- **In the browser,** the file manager (`/_myweb/`) shows the app: version,
  status, last check, notes, installed and published versions, "Check for
  updates", "use" per version and "Follow updates". Pages and tools get the same
  as JSON from `POST /cmd/app`: `{"check":true}` starts a check,
  `{"use":"1.3.0"}` or `{"use":"latest"}` switches, `{"available":true}` adds
  the published versions (changes are for admins only with myweb-team).
- **New signing key:** `myweb-publish rotate` makes a new key; releases are
  then signed with both and name both, and each myweb that installs one trusts
  the new key, with no change for users. Once they have updated,
  `myweb-publish rotate -finish` signs with the new key alone, and from that
  release on myweb no longer trusts the old one, even if given as `-web-key`.
  Users who skipped every release in between need the new public key once.
- **Only its own folders.** An installed app can read and change only the
  folders of the data folder its publisher declares (`myweb-publish build
  -folder /myapp`, signed with the release; without one, `/<name>`), which are
  created for it, and the ones you give it:

  ```sh
  myweb app access myapp                 # what it may use
  myweb app allow myapp /photos          # let it read a folder
  myweb app allow myapp /reports -write  # read and change one
  myweb app deny myapp /photos           # take it back
  myweb app allow myapp /                # everything, as before
  ```

  The rest of the data folder doesn't exist for it: listings, search, `/f/`,
  `/zip/`, `/events` and every command hide it, and it can't stop the server or
  change its own installation. For this the app and the file manager are kept
  on different addresses: the app at `http://127.0.0.1:PORT/`, the file manager
  at `http://localhost:PORT/_myweb/` (the old address redirects there). Scripts
  without a browser (curl), the file manager and `-cors` sites keep full
  access. This applies to apps installed with `-web-from`, `-app` or a
  catalog, not to your own `-web DIR`, and not under myweb-team, where
  people's permissions apply.
- **Safety:** myweb installs an app only if the manifest's ed25519 signature
  matches `-web-key` and the zip matches the signed SHA-256. There is no switch to skip this. It never moves to an older
  version by itself, refuses zips with paths that leave the app folder, accepts
  only https (http just for localhost), and keeps serving the installed version
  when the server can't be reached. A manifest can require a myweb version with
  `-requires-myweb`.
- Apps are installed in `~/.myweb/apps` (`C:\Users\<you>\.myweb\apps` on
  Windows), never in the data folder. Don't edit files there; they are replaced
  on update. The file manager stays at `/_myweb/`.

### App catalogs: install by name

A catalog is a signed list of apps, each with its manifest URL and public key,
so users install by name instead of by URL and key:

```sh
myweb app catalog              # the apps you can install (app catalog WORD searches)
myweb app install rx-graphic   # install one; or just: myweb app run rx-graphic
```

In the browser, the file manager's **App catalog** lists the same apps with
**Install** and **Open**; Open starts `myweb app run NAME` in the background
(logs in `~/.myweb/logs`) unless it is already running, and opens it.

myweb reads any catalog you add: `myweb app catalog add URL -key KEY` (`remove URL` drops
one). Catalogs are cached for an hour and used offline.

Publishing a catalog:

```sh
myweb-publish catalog init -title "My apps"       # apps.json, and catalog.key + .pub if missing
myweb-publish catalog add -manifest https://…/stable.json -app-key app.key.pub \
    -title "My app" -description "What it does"   # checks the manifest is signed with that key
myweb-publish catalog remove myapp
myweb-publish catalog list                        # check the signature
# each change signs apps.json again: upload apps.json and apps.json.sig together
```

The catalog only introduces an app: myweb checks the catalog's signature, then
installs and updates the app exactly as with `-web-from` and `-web-key`. An
installed app keeps the key it was installed with, so a later change to the
catalog can't redirect its updates. Catalogs and manifests must be https (http
only for localhost).

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
| `GET /zip/<dir>` | the folder as a `.zip`, streamed; `?f=name&f=…` only those entries of it. Leaves out `.myweb` and symbolic links |
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
| `catalog` | `{refresh}`, `{install}`, `{open}` | the catalogs' apps; install one; open one (starting it if needed, returns `url`) |
| `unzip` | `{path, to, onconflict}` | unpack a `.zip` (into a new folder next to it unless `to` is given); files land like uploads, `onconflict` as for `/up/`. Paths can't leave the folder; at most 100,000 files and 16 GiB |

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
  `https://mycellome.com`, `https://youdata.studio` and `https://cellcycle.org`
  and their subdomains (`myserver.DefaultCORS`), plus any given with `-cors`.
  Pages of other local servers are not allowed by default (any app another
  program serves on this machine would be too): give a dev server with e.g.
  `-cors http://localhost:5173`. A bare `*` is refused. The same list is checked
  for `/events`, and for every request that can change something (any method
  but GET, HEAD and OPTIONS): browsers send a plain form or text POST to
  another site without asking first, so myweb refuses those itself (403)
  unless they come from one of these pages. Clients that aren't browsers (no
  `Origin` header) are not affected.
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

**Releasing:** set the version in `myserver/server.go`, commit, then tag and
push it:

```sh
git tag -a v0.0.2 -m "myweb 0.0.2" && git push origin main v0.0.2
```

The release workflow (`.github/workflows/release.yml`) tests, builds myweb,
myweb-publish and myweb-team for Linux, macOS and Windows (amd64 and arm64)
with `scripts/release-build.sh`, and publishes them with `checksums.txt` as a
GitHub release; a tag like `v0.1.0-rc.1` makes a prerelease, which `myweb
upgrade` only installs when asked for by version.

## License

MIT, see [LICENSE](LICENSE).
