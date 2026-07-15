# Trove brand system

Trove's identity is an **open index**: an asymmetrical catalogue tab makes the
outer silhouette useful and recognisable, while a capital `T` is cut from its
centre. The mark describes a place where discovered facts are organised. It
does not imply that Trove controls the infrastructure it observes.

The master mark is one compound vector path. It has no background container,
gradient, effect, or second colour, so it remains clear at 16 px and can be
printed, cut, embroidered, or recoloured without reconstruction.

## Asset map

| File | Purpose |
| --- | --- |
| `trove-logo.svg` | Primary warm-paper horizontal lockup for README and light surfaces. |
| `trove-wordmark.svg` | Ink title-case wordmark, converted to paths. |
| `trove-mark.svg` | Violet transparent-background master mark. |
| `trove-app-icon.svg` | Warm-paper platform wrapper used to export PWA and touch icons. |
| `trove-icon-256.png` | Raster mark for directories and integrations. |
| `trove-social-card.svg` | Editable 1280×640 GitHub social-preview source. |
| `trove-social-card.png` | Canonical 1280×640 GitHub social preview, under 1 MB. |
| `trove-social-card.jpg` | JPEG fallback of the same card. |
| `export-assets.sh` | Rebuilds every raster export from the SVG masters. |

Dashboard copies live in `web/public/`: `trove-mark.svg`, the reversed
`trove-wordmark.svg`, `favicon.svg`, `favicon.ico`, and 180/192/512 px app
icons. SVG files are the source of truth; PNG, JPEG, and ICO files are exports.

## Palette

| Token | Value | Use |
| --- | --- | --- |
| Ink | `#191621` | Wordmark, dark fields, primary copy. |
| Trove violet | `#7657F6` | Primary mark on warm or light fields. |
| Dark-field violet | `#8B70FF` | Mark and interface accents on ink fields. |
| Paper | `#F6F0E5` | Light brand field and reversed wordmark. |
| Status green | `#72D987` | Healthy/live state only; never required by the logo. |

Ink on paper has a 15.7:1 contrast ratio. Dark-field violet on the dashboard
background has a 5.2:1 ratio. The primary violet on paper is reserved for the
large mark and other non-text graphics; body copy remains ink.

## Typography

The logo is title-case **Trove**, not uppercase `TROVE`. Its letterforms are
derived from Space Grotesk SemiBold and stored as vector paths, so the logo has
no runtime font dependency. Space Grotesk is an open-source typeface released
under the [SIL Open Font License 1.1](https://github.com/floriankarsten/space-grotesk/blob/master/OFL.txt).

The dashboard self-hosts Space Grotesk for display text, Inter for body text,
and JetBrains Mono for technical labels. These fonts reinforce the identity but
are not part of the mark itself.

## Usage

- Use a horizontal lockup when the available width is at least 240 px.
- Use the mark alone for avatars, favicons, stickers, and compact UI.
- Use the supplied app-icon wrapper only where a platform requires an opaque
  square; it is not a replacement for the container-free master mark.
- Keep clear space around the mark equal to the height of its raised index tab.
- Keep the negative-space `T` the same colour as the surface behind the mark.
- Use an approved one-colour conversion when violet is unavailable.
- Keep the ink wordmark on warm-paper or similarly light fields.
- Describe the full lockup as “Trove”. When the adjacent wordmark already names
  the project, the decorative mark should use empty alternative text.

Do not put the mark in a generic rounded-square tile, add platform logos, close
the index tab, fill the `T`, add gradients or glow, use green decoratively, or
recreate the wordmark as live text.

To set the GitHub card, upload `trove-social-card.png` under **Repository
settings → Social preview**. The supplied file is 1280×640, uses a solid
background, and stays below GitHub's 1 MB limit.

Run `./docs/brand/export-assets.sh` from any directory after changing a master
SVG. It requires `rsvg-convert` and either ImageMagick's `magick` command or
macOS `sips`; the script fails rather than silently leaving stale exports.
