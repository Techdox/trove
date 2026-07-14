# Trove brand assets

Trove's mark represents several reported catalogue layers resolving into one
small `T`: many sources, one read-only view. The geometry is deliberately flat
and remains recognisable in one colour and at favicon size.

## Asset map

| File | Purpose |
| --- | --- |
| `trove-logo.svg` | Primary horizontal lockup for the README and project pages. |
| `trove-wordmark.svg` | Wordmark without the symbol or background. |
| `trove-mark.svg` | Square master mark. |
| `trove-icon-256.png` | Raster mark for directories and integrations. |
| `trove-social-card.svg` | Editable social-card source. |
| `trove-social-card.png` | GitHub social preview, 1280×640, solid background, under 1 MB. |
| `trove-social-card.jpg` | JPEG fallback of the same 1280×640 card. |

Dashboard copies live in `web/public/`: `trove-mark.svg`,
`trove-wordmark.svg`, `favicon.svg`, `favicon.ico`, and the 180/192/512 px app
icons. The SVGs are the source of truth; raster files are exports.

## Palette

| Role | Value |
| --- | --- |
| Canvas | `#111019` |
| Mark tile | `#171522` |
| Border | `#34304A` |
| Primary mark | `#B7A1FF` |
| Status accent | `#98E889` |
| Wordmark | `#F3F0FF` |

The lime bar is optional. For one-colour use, render it in the same colour as
the lavender geometry. Do not add gradients, glow, drop shadows, outlines around
the symbol, or platform logos inside the mark.

## Usage

- Use the horizontal lockup when the available width is at least 240 px.
- Use the square mark below that width and for avatars, favicons, and app icons.
- Keep clear space around the mark equal to at least half the height of its top
  catalogue layer.
- Do not place the transparent wordmark on a light background; use the lockup or
  provide a sufficiently dark field.
- The social card is for link previews, not as the README logo.

To set the GitHub card, upload `trove-social-card.png` under
**Repository settings → Social preview**. GitHub recommends 1280×640 for best
display and requires the file to remain below 1 MB.
