#!/bin/sh
set -eu

# Rebuild every raster brand asset from the checked-in SVG masters.
# Requires librsvg's rsvg-convert plus either ImageMagick or macOS sips.

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
brand="$root/docs/brand"
public="$root/web/public"
tmp_favicon=$(mktemp "${TMPDIR:-/tmp}/trove-favicon.XXXXXX.png")
trap 'rm -f "$tmp_favicon"' EXIT HUP INT TERM

command -v rsvg-convert >/dev/null 2>&1 || {
  echo "error: rsvg-convert is required" >&2
  exit 1
}

rsvg-convert -w 256 -h 256 "$brand/trove-mark.svg" \
  -o "$brand/trove-icon-256.png"
rsvg-convert -w 1280 -h 640 "$brand/trove-social-card.svg" \
  -o "$brand/trove-social-card.png"

for size in 180 192 512; do
  rsvg-convert -w "$size" -h "$size" "$brand/trove-app-icon.svg" \
    -o "$public/trove-icon-$size.png"
done

rsvg-convert -w 32 -h 32 "$public/favicon.svg" -o "$tmp_favicon"

if command -v magick >/dev/null 2>&1; then
  magick "$brand/trove-social-card.png" -quality 90 \
    "$brand/trove-social-card.jpg"
  magick "$tmp_favicon" "$public/favicon.ico"
elif command -v sips >/dev/null 2>&1; then
  sips -s format jpeg -s formatOptions 90 "$brand/trove-social-card.png" \
    --out "$brand/trove-social-card.jpg" >/dev/null
  sips -s format ico "$tmp_favicon" --out "$public/favicon.ico" >/dev/null
else
  echo "error: magick or sips is required for JPEG and ICO exports" >&2
  exit 1
fi

echo "Trove brand exports rebuilt."
