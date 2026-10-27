# Plan: myweb for clients (desktop)

Status: proposal, nothing built yet.

## Goal

Someone who has never opened a terminal can install myweb, get apps from a store, open them, and keep
their files in a folder they know. They never see ports, flags, manifests or keys.

The command-line myweb stays as it is, for developers and power users. Both are the same program and
engine: the client experience is a set of screens and defaults on top. Nothing forks.

### Not in this plan

- Hosted or team use (myweb-team covers that).
- Per-app installers ("RX Graphic" as its own app). This is a possible later step; see the end.
- Changes to how apps are signed, installed or updated. That machinery stays as it is.

## The client's experience

1. They download **myweb** for their computer from a store's website and install it like any app: a
   `.dmg` on macOS, an installer on Windows.
2. On first start, two questions: **where should your files live?** (default `Documents/myweb`) and
   **start myweb when I log in?**
3. **Home** opens: their apps, and the apps their store offers. The build they downloaded may already
   know a store; otherwise they paste a store link or click "Add to myweb" on a store's website.
4. **Install**, then **Open**. The app opens in its own window.
5. Later, an app asks to use a folder: "*RX Graphic* wants to use *Figures*. Allow?"
6. Updates to myweb and its apps install by themselves, with a short "updated to 1.3" note.

## How it fits together

- **myweb-desktop** is the client program. It already serves the data folder, sits in the tray, starts
  at login and updates itself. It gains the Home window, the first-run wizard and link handling.
- **Home** is a page that myweb serves locally, like the file manager today. It grows out of the
  manager page's Apps and App catalog cards, which already install, open, pin versions and grant
  folders. It's served from myweb's own address only.
- **Windows:** the desktop opens Home and each app in an app-style window (the system's Chrome or Edge
  with `--app=URL`, falling back to the default browser). There's no address bar, and a port never
  shows.
- **Each app keeps its own port**, as it has since 0.0.9, so apps stay separate in the browser.

### Why Home is local, not on a store's website

Installing and starting apps writes to the client's disk and opens folders to apps. myweb refuses those
requests from other websites on purpose. A store's website only **links** to the local Home (see
below), and Home always asks the user before doing anything.

## Screens

### Home: My apps
- A grid of installed apps, with their catalog icons and names. Each has **Open**, and an **Update**
  badge when the app doesn't update itself.
- **⋯ menu:** About (description, version, release notes), Folders it can use, Previous version, Remove.
- **Empty state:** "No apps yet. Get apps →"

### Get apps
- The apps of every store, with icons, screenshots, descriptions and tags. It's searchable, with a
  filter by store when there are several.
- **Install**, or **Open** once installed. Each app shows which store it comes from.
- After a store changes, it shows the new list at once (catalogs are checked with their server each
  time they're read, since 0.0.8).

### App details
- **Description and screenshots.** The version, its release notes, and when it was updated.
- **Folders it can use**, in words: "Can read and change *Figures*". Each has a **Remove** button,
  plus **Allow another folder…** with a folder picker. The folders an app needs are listed as
  "Needed".
- **Version:** "Keep this version" (a pin) and "Go back to 1.2", for the versions still installed.
- **Remove app.** It asks whether to keep the app's files; by default they're kept.

### Stores
- Each store shows its name, its website's domain, how many apps it has, and when it was last checked.
- **Add a store:** paste a store link. The confirm step shows the name, the domain in large type, and a
  short key fingerprint: "Only add stores you trust."
- **Remove a store.** Its installed apps stay, and keep updating with the keys they were installed with.

### Settings
- **Files:** where they live, and Move… (the data folder).
- **Start at login** (already in the tray menu).
- **Updates:** install automatically (default on), or ask first.
- **Advanced:** the address and port, the command line, logs and diagnostics (`doctor`), and adding
  apps that aren't in a store. Closed by default.

### First run
1. Welcome.
2. Where should your files live?
3. Start at login?
4. Stores: the bundled store(s) are shown as "Apps from X". With none, offer "Paste a store link"
   or "Skip".
5. Home.

### Notices
Small notices in Home and in the tray menu: "RX Grid updated to 0.4.0 · What's new", "myweb will update
when you quit it", "An app couldn't update: Details".

## Store links

A store is two things: its catalog address and its public key. A **store link** carries both, plus a
display name, so a client never types a key.

- **Pastable form:** the catalog URL with the key in the fragment, e.g.
  `https://store.example/io/apps.json#key=<base64 key>&name=Example%20apps`. A URL fragment isn't sent
  to the server, it survives copying, and it reads as a link.
- **Clickable form, from a website:** `myweb://add-store?url=…&key=…&name=…`. The installed desktop
  registers the `myweb:` scheme and the running myweb receives the link.
- **Open an app from a website:** `myweb://app?store=<catalog url>&name=rx-graphic` opens the app's
  page in Home, with Install or Open. If the store isn't added yet, the link must also carry its key,
  and the user confirms adding the store first.
- **The command line accepts store links too:** `myweb app catalog add <store link>`.

The key still does its work: it verifies the catalog's signature, and each app's own key verifies its
updates. A link only saves typing. A wrong or tampered key fails verification, as today.

## Default stores, kept out of GitHub

The open-source build ships with **no store**. A release build for clients can include its own:

- **Build input:** a private setting in the release pipeline, e.g. the `MYWEB_DEFAULT_STORES` secret
  (a list of store links). It isn't in the repository.
- **Packaging:** the installer places a `stores.json` beside the program, or the release build embeds it.
- **First run:** if the home has no `catalogs.json`, myweb adds the bundled stores once. After that
  they're ordinary stores that the user can remove; a removed store doesn't come back.
- **Branded builds** for another store owner are the same build with different default stores, and
  later their own name and icon.

## Links and safety

- Home's install and start actions stay limited to myweb's own pages (as now). A `myweb:` link only
  opens Home on a confirm screen and never installs by itself.
- Adding a store always shows its domain and needs a click. Settings can turn off "Allow adding
  stores" for builds that should stick to their bundled stores.
- Apps not from a store (`-web-from` with a key) stay under Advanced.
- Nothing about signing changes: unsigned apps or catalogs are refused, with no switch to skip it.

## Installers and signing

Unsigned apps get scary warnings on macOS (Gatekeeper) and Windows (SmartScreen), and many clients stop
there. This is as important as the screens.

- **macOS:** a `.dmg` with `myweb.app`, signed with an Apple Developer ID and notarized. That needs an
  Apple Developer account, about $99 a year. The Mac build already runs on a macOS runner in the
  release workflow.
- **Windows:** an installer (MSI or NSIS) that is signed, with Start-menu and start-at-login entries.
  That needs a code-signing certificate.
- **Linux:** the existing binaries and the command line. A `.deb` or AppImage can come later if clients
  need it.
- **Self-update keeps working:** myweb-desktop already updates itself from the signed releases. The
  installer adds only the first install.
- **A download page per store** (on the store's own website) that picks the right installer for the
  visitor's system.

## Phases

| Phase | What ships | Done when |
|---|---|---|
| 1. Home | Home with My apps, Get apps and App details, built from the manager page's cards. Plain words, no ports. Opened from the tray menu. | A client can install, open, update, pin and remove apps and manage folders without the command line |
| 2. Stores | Store links (pasting them, and `app catalog add`). The Stores screen. Bundled default stores from the release-build setting. | A client build starts with its store; a pasted link adds another |
| 3. First run & settings | The wizard, Settings, automatic app updates, notices | A fresh install reaches Home in under a minute with two questions |
| 4. Installers & links | Signed `.dmg` and Windows installer, the `myweb:` scheme ("Add to myweb", "Open in myweb"), app windows | Download, double-click, done, with no security warnings |
| 5. Later | Per-app installers for a flagship app, under its own name and icon | Decided after phase 4, based on what clients ask for |

Phases 1–3 work without the installers and can be tried now with the existing tray app.

## Open questions

1. **Do clients mostly need files on their own computer?** This decides how much goes into the desktop
   compared with the web path.
2. **May clients add any store,** or only the bundled ones? This could be a per-build setting.
3. **App windows:** the system's Chrome or Edge in app mode (light, but needs one installed), or a
   built-in webview (heavier, the same everywhere)?
4. **Update policy:** should apps update automatically by default, or should a client pin a version
   for a project and keep it?
5. **The name clients see:** "myweb", or the store's brand, e.g. "Youdata"?
6. **Platforms for the first release:** macOS and Windows both, or one first?
