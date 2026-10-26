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

Rather not keep a terminal open? Use the [desktop app](#desktop-app-system-tray).

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
| `myweb share PATH [-upload] [-days N]` | a link to a folder or file that opens without the token (see below); `share list`, `share revoke ID` |
| `myweb doctor [-offline] [-fix]` | check the binary, home, config, data folder, port, servers, services, apps and catalogs, and say what to fix; `-fix` also does what is safe to |
| `myweb backup [FILE.zip] [-no-files]` | the home (config, apps, catalogs) and the data folder with its versions, trash and share links, in one `.zip` |
| `myweb restore FILE.zip [-force] [-dry-run] [-d DIR]` | put a backup back; files that differ are kept and listed unless `-force` |
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
it against the release's `checksums.txt`, whose signature must be by the
release key built into myweb, and that it runs, and replaces the
binary, keeping the old one next to it as `myweb.old` (`myweb upgrade
-rollback` swaps them back). Services installed with `myweb service install`
are restarted on the new binary (`-no-restart`: they aren't); other running
servers keep the old version until restarted. A running server tells, at most once a day, when a newer
release is out; `-no-upgrade-check` (or `myweb config set no-upgrade-check
true`) turns that off. A binary in a folder you can't write to (e.g.
`/usr/local/bin`) is upgraded with the rights to write there.
`myweb-team upgrade` and `myweb-publish upgrade` do the same for those
tools, from the same releases (from 0.0.4 on; `version` prints theirs).

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
| `-https` | | serve HTTPS with a certificate made for this machine (see [HTTPS](#https-and-tunnels)) |
| `-cert FILE`, `-key FILE` | | serve HTTPS with your own certificate |
| `-tunnel` | | also reach it from anywhere through a Cloudflare quick tunnel (turns on `-auth`) |
| `-versions N` | `20` | previous versions kept per file on overwrite (`0`: off) |
| `-trash-days N` | `30` | days deleted files stay in the trash (`0`: delete immediately) |
| `-upload-max MIB` | `100` | largest file accepted by an upload |
| `-backup-every TIME` | off | back up the home and data folder this often (`1d`, `12h`, `1w`); see [Backup](#backup-and-restore) |
| `-backup-keep N` | `7` | automatic backups kept |
| `-backup-to DIR` | `~/.myweb/backups` | where automatic backups go |
| `-log FILE` | | also append requests and messages to FILE as JSON lines (rotates at 50 MB) |
| `-no-tui` | | plain log output |
| `-no-open` | | don't open the browser on start |
| `-new` | | start another copy even if one already serves this folder and app |
| `-other-apps` | true | also serve the other installed apps, each on a port of its own |
| `-no-upgrade-check` | | don't look for a newer myweb release (checked once a day) |
| `-v`, `-version` | | print the version |

## Terminal UI

| Key | |
|---|---|
| `?` | every key, and what it does here |
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

## Desktop app (system tray)

`myweb-desktop` is myweb in the system tray (the menu bar on macOS). While it
runs, it serves your data folder, and opens the file manager on start. Its
menu:

- **Open file manager** (or the app served at `/`), and **Apps**: the other
  installed apps, signed in.
- **Show data folder**, **Choose data folder…** (kept in
  `~/.myweb/desktop.json`; until you choose one, it serves myweb's default,
  as plain `myweb` does), **Stop server** / **Start server**.
- **Start at login**: a launchd agent on macOS, an entry in
  `~/.config/autostart` on Linux, the `Run` registry key on Windows. Started
  at login, it serves without opening the browser.
- **Show log**: `~/.myweb/logs/desktop.log`, which also holds the server's
  output.
- **Check for updates**: it also checks once a day, and then offers
  **Update to myweb VERSION**, which installs that release (checked against
  its signed checksums, as `myweb upgrade` does) and restarts into it. On
  macOS it replaces the whole `myweb.app`, keeping the previous one as
  `myweb.app.old`. `myweb-desktop upgrade [-check | VERSION]` does the same
  from a terminal, and a running tray restarts into the new version.
- **Quit myweb** stops the server it started. A server it found already
  serving the folder (`myweb service`, another terminal) keeps running.

It shares `~/.myweb` (the config, apps, catalogs and share links) with the
`myweb` command, and `myweb status`, `open` and `stop` see its server. Only
one runs at a time: starting it again opens the file manager. With
arguments, it is the myweb command line (`myweb-desktop status`), except on
Windows, where it has no console to print to.

From the [releases](https://github.com/nimezhu/myweb/releases):
`myweb-desktop_VERSION_darwin_universal.zip` holds `myweb.app` (not
notarized: on first open, right-click it and choose Open);
`myweb-desktop_VERSION_windows_ARCH.exe`; `myweb-desktop_VERSION_linux_ARCH`,
which shows in trays that support StatusNotifierItem (KDE, and GNOME with
the AppIndicator extension, as on Ubuntu). To build it:

```sh
go build ./cmd/myweb-desktop               # Linux, or Windows with -ldflags -H=windowsgui
scripts/desktop-mac.sh 0.0.5               # on a Mac: dist/desktop/…_darwin_universal.zip
```

## File manager

At `/` (or `/_myweb/` when `-web` is set): browse folders, preview files
(images, video, audio, PDF, Markdown, CSV/TSV, JSON, text; HTML is shown as
source and never run; opened at `/f/`, an HTML or SVG file runs in a sandbox
with no rights on the server, unless `-trust-html`), search names and contents, upload files and whole
folders by dropping them on the list, make folders, rename, move files by
dragging them onto a folder (or a folder in the path above), tick several to
move or delete them at once (a folder with everything in it), browse and
restore previous versions, and restore files from the trash. Images show a
thumbnail. Text files (Markdown, CSV, JSON, code) can be edited in their
preview: a save keeps the old content as a version, and if someone changed
the file meanwhile it asks before replacing their change. Files whose name
starts with a dot (`.DS_Store`, the `._name` files macOS leaves over WebDAV)
are hidden unless **hidden files** is ticked. Download a folder, or the
files and folders you tick, as one `.zip`, and extract a `.zip` into a new
folder next to it. It updates live as files
change, and lists and runs the plugin commands.

- **Copy**: *Copy to…* and *Duplicate* in a file's ⋯ menu, *Copy…* for the
  ticked ones; a name that's taken gets a new one (`a (1).txt`).
- **Folder sizes** (⋯ → Folder sizes) adds up the folder shown and each
  folder in it, biggest first, and how much versions, trash and thumbnails
  take. The status dot says how much of the disk is free.
- **Recent** lists the changes made through myweb (here, the API, the
  network drive and share links), newest first, by whom on myweb-team; a
  path opens its folder with it selected.
- **Versions → Compare** shows what changed in a text file, line by line,
  between a version and now (or the version before it), and restores it.
- **On a phone**, open `/_myweb/` (over HTTPS, see [HTTPS and tunnels](#https-and-tunnels))
  and *Add to Home screen* / *Install app*: it opens like an app. On Android
  it is then in the **Share** menu: share photos or files to myweb, choose a
  folder, and they are uploaded.

**Installed apps** lists every installed app with its version, the folders
it may use and where this server serves it; there you open one, check it
for updates, allow it a folder (read only, or read and write) or take one
back, and remove it, as `myweb app …` does in a terminal. Changes are for
this server's own pages (and, on myweb-team, admins).

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
  change its own installation. A symbolic link in its folders leads it only
  to where it may go anyway. For this the app and the file manager are kept
  on different addresses: the app at `http://127.0.0.1:PORT/`, the file manager
  at `http://localhost:PORT/_myweb/` (the old address redirects there). Scripts
  without a browser (curl), the file manager and `-cors` sites keep full
  access. A browser that says neither where a request comes from nor which
  site it is (Safari before 16.4; any browser over plain http to an address
  other than localhost) is taken to be the app on the app's address. This
  applies to apps installed with `-web-from`, `-app` or a catalog, not to your
  own `-web DIR`. Under myweb-team an app gets a port of its own (the file
  manager stays at `/`, `/apps/NAME/` leads to the app) and whoever is signed
  in uses it with their own permissions, only within the app's folders.
- **Safety:** myweb installs an app only if the manifest's ed25519 signature
  matches `-web-key` and the zip matches the signed SHA-256. There is no switch to skip this. It never moves to an older
  version by itself, refuses zips with paths that leave the app folder, accepts
  only https (http just for localhost), and keeps serving the installed version
  when the server can't be reached. A manifest can require a myweb version with
  `-requires-myweb`.
- Apps are installed in `~/.myweb/apps` (`C:\Users\<you>\.myweb\apps` on
  Windows), never in the data folder. Don't edit files there; they are replaced
  on update. The file manager stays at `/_myweb/`.

### Several apps at once

One myweb serves every installed app, each on a port of its own that it
keeps (so what the browser stores for it stays), next to the files or the app
at `/`:

```
http://localhost:8433/_myweb/      the file manager
http://127.0.0.1:8434/             rx-graphic (also: http://127.0.0.1:8433/apps/rx-graphic/)
http://127.0.0.1:8435/             another app
```

The terminal and `myweb status` show them; `myweb open rx-graphic` opens one,
and apps installed or removed while myweb runs are served or dropped right
away. Each app is a separate site to the browser, limited to its own folders,
so apps can't reach each other's data. `-other-apps=false` serves only the
app at `/`.

### App catalogs: install by name

A catalog is a signed list of apps, each with its manifest URL and public key,
so users install by name instead of by URL and key:

```sh
myweb app catalog              # the apps you can install (app catalog WORD searches)
myweb app install rx-graphic   # install one; or just: myweb app run rx-graphic
```

In the browser, the file manager's **App catalog** shows the same apps as
cards (icon, description, tags, screenshots; search and filter by tag) with
**Install** and **Open**. In the terminal UI, `i` lists them: ⏎ installs or
opens. An installed app is served by the running myweb right away; with
`-other-apps=false`, Open starts `myweb app run NAME` in the background (logs
in `~/.myweb/logs`).

myweb reads the catalogs you add, none until then: `myweb app catalog add URL
-key KEY` (`remove URL` drops one). Catalogs are cached for an hour and used offline.

Publishing a catalog:

```sh
myweb-publish catalog init -title "My apps"       # apps.json, and catalog.key + .pub if missing
# add checks the manifest is signed with that key; images may be links relative
# to apps.json; adding an app again keeps what isn't given
myweb-publish catalog add -manifest https://…/stable.json -app-key app.key.pub \
    -title "My app" -description "What it does" \
    -icon img/myapp.png -screenshot img/myapp-1.png -tags graphics,editor
myweb-publish catalog remove myapp
myweb-publish catalog list                        # check the signature
# each change signs apps.json again: upload apps.json and apps.json.sig together
```

The catalog only introduces an app: myweb checks the catalog's signature, then
installs and updates the app exactly as with `-web-from` and `-web-key`. An
installed app keeps the key it was installed with, so a later change to the
catalog can't redirect its updates. Catalogs and manifests must be https (http
only for localhost).

## Network drive (WebDAV)

The data folder is also served as a network drive at
`http://127.0.0.1:PORT/dav/` (the terminal UI shows the address; `-no-dav`
turns it off). Connect to it from:

- **macOS Finder:** Go › Connect to Server, `http://127.0.0.1:8433/dav/`
- **Linux:** Files › Other Locations, `dav://127.0.0.1:8433/dav/`, or `davfs2`
- **Windows Explorer:** Map network drive, `http://127.0.0.1:8433/dav/`
  (Windows sends a password only over HTTPS, so with a token use `-https`
  with a certificate Windows trusts, or a WebDAV app)
- **rclone, Cyberduck, phone file apps:** a WebDAV remote at that address

With a token, the password is the token (any user name). Reading needs no
password on a server that only this machine reaches, as with `/f/`. Through
an SSH tunnel (`ssh -L 8433:127.0.0.1:8433 server`) the remote data folder
becomes a drive on your machine; forward the same port number.

It keeps the rules of the rest of myweb: a saved file lands whole and the
one it replaces is kept as a version, deleted files (and a deleted folder's
files) go to the trash, `.myweb` is hidden, read-only mode refuses changes,
and on myweb-team people's permissions apply. Apps' pages and share links
can't use it.

## Share links

A share link opens one folder or file for someone who has nothing else, no
token and no account, until it expires (7 days unless `-days N`, at most a
year) or you revoke it:

```sh
myweb share /photos                # browse it, or download it all as a .zip
myweb share ~/data/report.pdf      # a file on disk inside the data folder works too
myweb share /inbox -upload         # a drop box: people send files, can't see any
myweb share /report.pdf -password  # asks for a password before opening (prompted)
myweb share /data.zip -max-downloads 3   # ends after 3 downloads
myweb share list                   # ID, path, kind, expiry, downloads / what it received
myweb share url 3f9c1e07ab         # the link again
myweb share revoke 3f9c1e07ab
```

In the file manager, **share** on a row fills in the **Share links** panel,
which also lists them, copies them again and revokes them. Links are kept in
`.myweb/shares.json` (with their secret, so whoever made one can copy it
again; on myweb-team others, admins included, can't) and out of the request
log. A password is kept only as a salted PBKDF2 hash; a browser that gives it
gets a cookie for that link only, and after 10 wrong passwords in 10 minutes a
link refuses more for a while. A link reaches only its folder, never `.myweb` or where a
symbolic link inside leads elsewhere; shared HTML runs in a sandbox; an upload
never replaces a file (it becomes `name (1).ext`), and a drop box takes at
most 1024 MB and 500 files in all (`-max-mb N`, `-max-files N`; `share list`
shows what it has taken). Other machines can open a link only when the
server listens on the network (`-global`) or through `-tunnel`, whose
address links then use.
Links are at `/_s/<secret>/`; the commands are `/cmd/shareCreate`, `/cmd/shares`
and `/cmd/shareRevoke`, which apps may not use.

## HTTPS and tunnels

On your own machine plain HTTP is fine. On a network, the token, sign-in
links, share links and WebDAV passwords would travel in clear text, so serve
HTTPS:

```sh
myweb -global -https -auth               # a certificate made for this machine
myweb -global -cert c.pem -key k.pem     # yours: mkcert, `tailscale cert`, a CA
```

`-https` makes a certificate for `localhost`, `127.0.0.1` and, with
`-global`, this machine's name and network addresses, and makes a new one
before it ends or when the addresses change. It is signed by a small CA of
myweb's own, made once and kept ten years in `~/.myweb/tls/`, so you trust
that once per device and browsers stop warning:

```sh
myweb tls            # the CA, its SHA-256, whether this machine trusts it
myweb tls trust      # this machine trusts it (asks for your password / sudo)
myweb tls untrust    # and no longer
```

On other devices (phones, other computers) open
`https://<this machine>:PORT/_myweb/cert` (the terminal UI shows it), check
the SHA-256 against `myweb tls`, and follow the steps there for macOS,
Windows, iPhone, Android and Firefox. Windows needs it for WebDAV over HTTPS.
The CA can sign only local names (`localhost`, `.local`, `.lan`, `.internal`,
`home.arpa` and this machine's name), so even its key can't stand in for a
website; keep `~/.myweb/tls` private all the same. Until a device trusts it,
check that the SHA-256 the browser shows matches the one myweb prints.
`myweb status`, `stop`, `share` and the other commands talk to an HTTPS
server by its certificate's fingerprint.

`-tunnel` also makes the server reachable from anywhere, through a
[Cloudflare quick tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/do-more-with-tunnels/trycloudflare/)
(install `cloudflared`; no account needed). The terminal UI and the log show
its `https://….trycloudflare.com` address, which changes every start and
ends with the server. The token is always on then, and reading needs it too;
share links use the tunnel's address. Only the main port is tunnelled: while
an app runs at `/`, the file manager through the tunnel is limited to the
app's folders, since it is the same site as the app there.

Without `-global` or `-tunnel`, myweb answers only requests addressed to
`localhost` or `127.0.0.1`, so a web page can't reach it by pointing its own
name at `127.0.0.1` (DNS rebinding).

## Backup and restore

```sh
myweb backup                       # myweb-backup-DATE-TIME.zip in this folder
myweb backup ~/b.zip -no-files     # settings, versions, trash and share links only
myweb restore ~/b.zip -dry-run     # what it would do
myweb restore ~/b.zip              # e.g. on a new machine
```

A backup holds the home folder (config, installed apps, catalogs; not the
records of running servers) and the data folder, with its `.myweb` (versions,
trash, share links). The home's own data folder goes back into the home it is
restored to; a data folder elsewhere goes back where it was, or to `-d DIR`.
Restore writes each file whole, keeps its time, and never replaces a file that
differs unless `-force` (it lists them); it refuses while a server runs from
the home. The zip is readable only by you, and holds the token: keep it
private. Symbolic links are left out.

**Automatic backups.** A running myweb (or its service) can back up by itself:

```sh
myweb backup -every 1d                           # daily, into ~/.myweb/backups, keeping 7
myweb backup -every 12h -keep 14 -to /mnt/usb/myweb
myweb backup -list                               # the schedule and the backups made
myweb backup -every off
```

This sets `backup-every`, `backup-keep` and `backup-to` in the config, for
servers and services started after it. A backup is made when the newest is
that old, a minute after a start and then when due; several servers of one
home take turns. A folder on another disk also keeps them if this disk
fails. `myweb doctor` says when the last one failed or is older than it
should be.

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
- **HTTPS:** over a network, links and cookies travel in clear text unless
  you serve HTTPS: `myweb-team serve -global -https` (or `-cert`/`-key`, see
  [HTTPS](#https-and-tunnels)), a reverse proxy such as
  [Caddy](https://caddyserver.com), or a private network such as Tailscale.

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
| `/dav/<path>` | the data folder over WebDAV (`PROPFIND`, `GET`, `PUT`, `MKCOL`, `MOVE`, `COPY`, `DELETE`, `LOCK`); see Network drive |
| `GET /zip/<dir>` | the folder as a `.zip`, streamed; `?f=name&f=…` only those entries of it. Leaves out `.myweb` and symbolic links |
| `GET /thumb/<path>?w=256` | a small JPEG of an image (PNG when it has transparency), its longest side `w` (64, 128, 256, 512 or 1024; rounded up). Reads JPEG (turned upright by its EXIF orientation), PNG, GIF and WebP; an image already that small comes as it is. Made once and kept in `.myweb/thumbs` (left out of backups) |
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
| `cp` | `{from, to, overwrite, rename}` | copy a file or folder (keeps modification times; leaves out symbolic links); `rename` picks a free name (`a (1).txt`) when `to` is taken; returns where it went |
| `du` | `{path}` | the size and file count of a folder and of each folder in it; for `/`, also what versions, trash and thumbnails take |
| `activity` | `{limit, dir}` | changes made through the server, newest first: uploads, saves and deletes close in time grouped by folder (`op`, `path`, `to`, `n` files, `count`, `who`, `via`) |
| `rm` | `{paths}` | delete files and folders; everything in them goes to the trash |
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
(version, read-only and sign-in state; signed in, the disk's free and total bytes), `POST /cmd/app` (the installed web app,
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

- **Local by default.** Only `127.0.0.1` can connect unless `-global` is given,
  and only requests addressed to `localhost` or `127.0.0.1` are answered
  (against DNS rebinding). `-https` encrypts, `-tunnel` reaches it from
  outside with the token always on (see [HTTPS](#https-and-tunnels)).
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

**Releasing:** write the version's section in [CHANGELOG.md](CHANGELOG.md)
(it becomes the release notes), set the version in `myserver/server.go`,
commit, then tag and push it:

```sh
git tag -a v0.0.2 -m "myweb 0.0.2" && git push origin main v0.0.2
```

The release workflow (`.github/workflows/release.yml`) tests, builds myweb,
myweb-publish and myweb-team for Linux, macOS and Windows (amd64 and arm64)
with `scripts/release-build.sh` (and myweb-desktop: for Linux and Windows
there, and `myweb.app` on a Mac with `scripts/desktop-mac.sh`, which
`.github/workflows/desktop.yml` also runs when its code changes), signs `checksums.txt` with the release key
(the repository secret `MYWEB_RELEASE_KEY`; its public key is
`selfupdate.Keys`), and publishes them as a GitHub release; a tag like `v0.1.0-rc.1` makes a prerelease, which `myweb
upgrade` only installs when asked for by version.

## License

MIT, see [LICENSE](LICENSE).
