# myweb's icon

A home holding apps. Each PNG is rendered from one of these SVGs (in a
browser, which draws the gradients and masks right):

- `icon.svg`: `manager/web/icon-512.png`, `icon-192.png`, and so myweb.app's
  icon (`scripts/desktop-mac.sh`).
- `bleed.svg` (180 px, no transparency): `apple-touch-icon.png`;
  `maskable.svg` (512 px, no transparency): `icon-maskable-512.png`.
- `small.svg`, `offsmall.svg` (solid tiles, to read at 16 px): the tray's
  `on.png`/`off.png` (64 px) and `on.ico`/`off.ico` (16, 24, 32, 48).
- `on-template.svg`, `off-template.svg` (44 px): the macOS menu bar's.

`off.svg` is the grey one at large sizes. The pages' logo and tab icon are
the same drawing, inline in `manager/web/*.html`.
