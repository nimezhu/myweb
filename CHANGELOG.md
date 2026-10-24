# Changelog

Each release's section here is its notes on GitHub, and what `myweb upgrade`
prints after installing it.

## Unreleased

- The data folder is a network drive: WebDAV at `/dav/` for Finder,
  Explorer, Linux file managers, rclone and phone apps. Saves keep versions,
  deletes go to the trash, `.myweb` is hidden; the token is the password.
  `-no-dav` turns it off.
- myweb-team limits installed apps too: an app gets a port of its own, and
  each person uses it with their own permissions, only within the app's
  folders.
- HTML and SVG files of the data folder always run in a sandbox when opened,
  not only while an app runs (`-trust-html` lets them run as the server,
  when no app does).
- Browsers that don't send `Sec-Fetch-Site` (Safari before 16.4, or plain
  http on a LAN) are no longer taken for the file manager on an app's address.
- `myweb backup` puts the home (config, apps, catalogs) and the data folder
  with its versions, trash and share links in one `.zip`; `myweb restore`
  puts it back, never replacing a file that differs unless `-force`.
- The file manager makes folders, moves files by dragging them onto a folder,
  moves or deletes several ticked files and folders at once (a folder with
  everything in it, to the trash), and shows image thumbnails. `/cmd/rm`
  deletes files and folders.
- `?` in the terminal UI lists every key.
- `myweb doctor -fix` also fixes what is safe to: makes the home folder,
  removes old binaries left by upgrades, restarts services that stopped or
  run an old version, installs an app's missing version.
- An upload link takes at most 1024 MB and 500 files in all (`share -upload
  -max-mb N -max-files N`); `share list` shows what each has received.

## 0.0.3

- Share links: `myweb share PATH` (or **share** in the file manager) makes a
  link that opens a folder or file without the token, until it expires or is
  revoked; `-upload` makes a drop box for files. `share list`, `share revoke`.
- `myweb doctor` checks the setup (binary, PATH, version, home, config, data
  folder, port, running servers and services, apps, catalogs) and says what
  to fix.
- `myweb upgrade` installs only releases signed with myweb's release key
  (`checksums.txt.sig`), so a GitHub release that was tampered with is
  refused. `myweb-publish sign` signs a file the same way.
- A symbolic link in an app's folder no longer lets the app reach where it
  leads, unless that is one of the app's folders too.

## 0.0.2

From 0.0.1, which can't upgrade itself, download your system's binary below
(or `go install github.com/nimezhu/myweb@v0.0.2`); from then on, `myweb
upgrade` does it.

**Changes to know about before upgrading**

- While an installed app is served at `/`, the file manager moves to
  `http://localhost:PORT/_myweb/`; the app keeps `http://127.0.0.1:PORT/`.
  Old file-manager links redirect. Browsers keep the two addresses apart, so
  the app can't act with the file manager's rights.
- Installed apps may use only their own folders: the ones their manifest
  declares, else `/<app name>`. Give more with `myweb app allow APP FOLDER`
  (`-write` to write too); see them with `myweb app access APP`.
- Pages of other local servers (e.g. a dev server at `http://localhost:5173`)
  may no longer call the API: add them with `-cors`.
- Writes from other websites are refused, with or without a token.
- HTML and SVG files in the data folder run in a sandbox while apps are limited.

**New**

- `myweb upgrade` installs a release from GitHub, checked against its
  checksums; `-check`, a `VERSION`, `-rollback`. Installed services are
  restarted with the new binary (`-no-restart`: don't). A running server
  tells once a day when a newer release is out.
- `myweb status`, `myweb open`, `myweb stop`: the servers running now.
- Download folders and files as a `.zip`, and extract uploaded zips, in the
  file manager and with `GET /zip/<folder>`.
- App catalogs: `myweb app install NAME` installs from a signed list of apps;
  `myweb app catalog add URL -key KEY` adds one, `myweb app catalog` lists
  and searches them. The
  file manager and the terminal UI (`i`) show the apps with their icons, tags
  and screenshots, to install and open. `myweb-publish catalog` makes and
  signs a catalog.
- Every installed app is served at once, each on a port of its own;
  `/apps/NAME/` redirects to it (`-other-apps=false`: only the one at `/`).
- `myweb-publish init -folder PATH[:ro]` declares the folders an app uses.

**Fixed**

- `myweb upgrade` on Windows no longer fails when the binary replaced by the
  previous upgrade is still running.
