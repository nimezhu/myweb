# Changelog

Each release's section here is its notes on GitHub, and what `myweb upgrade`
prints after installing it.

## 0.0.5

Changes to know before upgrading:

- New built-in commands `cp`, `du` and `activity`: a Go program that embeds
  myserver and registers its own command by one of these names now panics
  at start (as for any name registered twice); rename yours.
- The data folder's `.myweb` holds two more things: `thumbs/` (thumbnails,
  made again when needed, left out of backups) and `activity.jsonl` (the
  Recent view's log).
- The file manager registers a service worker at `/_myweb/`, for installing
  it as an app; it handles only files shared to it.
- Extracting a `.zip` leaves out macOS's `._name` files too.

New:

- The file manager is laid out anew: Files fill the page, and Apps, Shares,
  Trash and Developer (the API, commands and "write a file") are tabs, each
  with its own address (`#apps`, …). One status dot in the header holds the
  version, sign-in, live updates, a light/dark/auto theme and Stop server.
- Files: a ⋯ menu on each row (and right-click) instead of a row of links;
  rename in place; Move to… with a folder picker; a toolbar for the
  selection; sortable columns; a filter that narrows the folder as you type
  (Enter still searches inside files); icons by kind; a grid view with
  thumbnails; New → Text file opens it in the editor.
- Deleting asks nothing when the trash is on and offers Undo instead; with
  `-trash-days 0` it asks first. `rm` reports the trash ids it made.
- Keyboard: arrows, Enter, Space to preview, x to select, F2, Delete,
  Backspace for the folder above, / to filter, ← → between files in a
  preview, ? for the list.
- Uploads: drop files anywhere on the page; a panel shows each file's
  progress and whether it was renamed.
- No browser prompt or confirm boxes are left; messages are toasts. The
  control page at `/ctrl/` matches the rest.
- Thumbnails: `GET /thumb/<path>?w=256` makes a small JPEG (PNG with
  transparency) of a JPEG, PNG, GIF or WebP image once and keeps it in
  `.myweb/thumbs`; JPEGs are turned upright by their EXIF orientation. The
  file manager's list and grid use them instead of the originals, so a
  folder of photos costs a few KB each to show, not megabytes. Backups
  leave them out.
- Big folders: the file manager shows 200 entries at a time and more as
  you scroll either way; End, Home and the arrows jump without drawing the
  rest. A folder of 50,000 files opens in a fraction of a second.
- Copy: `cp` copies files and folders (`rename` picks a free name); *Copy
  to…* and *Duplicate* in the file manager.
- Folder sizes: `du` adds up a folder and each folder in it (and, for the
  whole data folder, what versions, trash and thumbnails take); ⋯ → Folder
  sizes shows them, biggest first. `/cmd/status` and the status dot show
  the disk's free space; `myweb doctor` warns when it runs low.
- Automatic backups: `myweb backup -every 1d [-keep 7] [-to DIR]` makes a
  running server back up by itself (`-backup-every`, `-backup-keep`,
  `-backup-to`); `backup -list` shows them, `doctor` warns when the last one
  failed or is old.
- Recent: every change made through the server (file manager, API,
  WebDAV, share links) is logged in `.myweb/activity.jsonl`; the `activity`
  command and the **Recent** tab show them, grouped, newest first, by whom.
- Versions → Compare: what changed in a text file between a version and now,
  line by line.
- Install the file manager on a phone's home screen (`/_myweb/`, over
  HTTPS); on Android, share files to it from other apps to upload them.

## 0.0.4

Changes to know before upgrading:

- HTML and SVG files of the data folder always open in a sandbox now, not
  only while an app runs; `-trust-html` lets them run as the server (when no
  app does).
- Without `-global` or `-tunnel`, only requests addressed to localhost or
  127.0.0.1 are answered, so a web page can't reach the server by pointing
  its own name at 127.0.0.1 (DNS rebinding).
- myweb-team limits installed apps too: an app gets a port of its own, and
  each person uses it with their own permissions, only within the app's
  folders.

New:

- HTTPS: `-https` serves with a certificate made for this machine, signed by
  a local CA of myweb's own that may sign only local names; `-cert`/`-key`
  serve yours. `myweb tls trust` makes this machine trust the CA, and
  `/_myweb/cert` offers it to phones and other computers with the steps.
- `-tunnel` also makes the server reachable from anywhere through a
  Cloudflare quick tunnel (needs cloudflared); the token is always on then,
  and share links use its address.
- The data folder is a network drive: WebDAV at `/dav/` for Finder,
  Explorer, Linux file managers, rclone and phone apps. Saves keep versions,
  deletes go to the trash, `.myweb` is hidden; the token is the password.
  `-no-dav` turns it off.
- `myweb backup` puts the home (config, apps, catalogs) and the data folder
  with its versions, trash and share links in one `.zip`; `myweb restore`
  puts it back, never replacing a file that differs unless `-force`.
- Share links: `-password` (a salted hash is kept; wrong guesses are
  limited), `-max-downloads N`, `share url ID` and **copy** in the file
  manager show a link again, and `share list` shows downloads. An upload
  link takes at most 1024 MB and 500 files in all (`-max-mb N -max-files N`).
- The file manager:
  - edits text files in their preview (a save keeps a version and asks
    before replacing someone else's change);
  - makes folders, moves files by dragging them onto a folder, moves or
    deletes several ticked files and folders at once, and shows image
    thumbnails;
  - hides dotfiles such as `.DS_Store` and macOS's `._name` unless
    **hidden files** is ticked;
  - has an **Installed apps** panel: every app's version, folders and
    address; open, check for updates, allow or take back folders, remove.
- `myweb-team upgrade` and `myweb-publish upgrade` install those tools from
  the same signed releases as `myweb upgrade` (from this version on); both
  have a `version` command.
- `myweb doctor -fix` also fixes what is safe to: makes the home folder,
  removes old binaries left by upgrades, restarts services that stopped or
  run an old version, installs an app's missing version.
- `?` in the terminal UI lists every key; `/cmd/rm` deletes files and
  folders.

Fixed:

- Browsers that don't send `Sec-Fetch-Site` (Safari before 16.4, or plain
  http on a LAN) are no longer taken for the file manager on an app's address.
- The terminal UI showed the WebDAV address with port 0.

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
