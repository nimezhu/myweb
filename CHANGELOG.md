# Changelog

Each release's section here is its notes on GitHub, and what `myweb upgrade`
prints after installing it.

## Unreleased

- The data folder is a network drive: WebDAV at `/dav/` for Finder,
  Explorer, Linux file managers, rclone and phone apps. Saves keep versions,
  deletes go to the trash, `.myweb` is hidden; the token is the password.
  `-no-dav` turns it off.

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
