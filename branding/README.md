# Telesphorepo brand

The mark is a gold admission ticket. A forward-leaning letter T and a column of perforation slots
are punched through it, and a semicircular notch is cut into each short edge. The brand has two
tiers:

- The **flat mark** is for small and functional sizes: the favicon, the app header, docs and
  lockups. Use it at 32 px and below without exception.
- The **felt ticket** is for large hero uses: the website hero, the sign-in page, empty states
  and the social (OG) image. It has the same geometry as the flat mark, at 10x, made of matte gold
  wool felt with the T pressed into it.

## Colors

| Name | Hex | Use |
|---|---|---|
| Gold | `#E9B546` | The mark on dark backgrounds. 9.18:1 on Carbon. |
| Gold Deep | `#A8760F` | The mark on light backgrounds and in the favicon. 3.99:1 on white. |
| Carbon | `#131838` | The brand background. The wordmark on light backgrounds. |
| Paper | `#F5F1E6` | The wordmark on Carbon. |

Gold is 1.88:1 on white, which is too low for a graphic. On a light background, use Gold Deep.

The felt ticket also uses these internal tones. Do not use them as brand colors.

- Felt light: `#F7C766`. Flat felt under the light comes out near `#D9A64A`.
- Fibers: light `#FFEBB8`, dark `#734707`. Edge shade: `#6B4508`.
- Pressed T: floor `#7A520E`, inner shadow `#4A2E04`, inner light `#FFE3A0`.
- Printed ink: `#4E3307`. Shadow: `#05071A`.

## The T

The T is the Telesphoreo T: it leans forward. The bar is a parallelogram whose two ends are cut on
the same 45-degree diagonal, up to the right. The stem hangs straight from under the bar, and its
bottom is cut on a 45-degree diagonal, so the stem ends in a point at the lower left.

The proportions come from one stroke width s:

- The bar is s tall. Its top and bottom edges are 4s long, and the top edge starts s to the right
  of the bottom edge.
- The stem is s wide. It is centered under the middle line of the bar.
- The left side of the stem is 3s long below the bar. The right side is 2s long, then the cut
  runs down to the left at 45 degrees to the point.

The T is 5s wide and 4s tall.

## Construction of the flat mark

All shapes are on an integer grid in `viewBox="0 0 64 40"`. The file is one `<path>` with
`fill-rule="evenodd"`. The T and the slots are subpaths inside the outline, so they are real
holes: the background shows through. There are no strokes and no transforms.

| Part | Geometry |
|---|---|
| Ticket body | Rect x 0..64, y 0..40. Corner radius 4. |
| Notches | Two circles of radius 5. The centers are (0, 20) and (64, 20), on the edges at the vertical middle. |
| Perforation | 6 slots, each 2 wide and 4 tall, with fully round ends (radius 1). The slots are at x 44..46 (center x = 45). The gap between slots is 2. |
| T bar | Parallelogram, y 6..13. Top edge x 12..40, bottom edge x 5..33. |
| T stem | x 19..26, from y 13. The left side ends at y 34, the right side at y 27. The cut runs from (26, 27) to (19, 34). |

The T uses s = 7. Its area is 318.5 square units, the same as the upright T of the first mark
(320), so the mark keeps its weight.

The arithmetic:

- **Perforation, vertical.** The column height is 6 × 4 + 5 × 2 = 34. It starts at (40 − 34) / 2 = 3
  and ends at 37. The column center is (3 + 37) / 2 = 20, the ticket middle.
  The slots are at y 3, 9, 15, 21, 27 and 33.
- **T, vertical.** The T is 4 × 7 = 28 tall. It starts at (40 − 28) / 2 = 6 and ends at 34. The space
  is 6 above and 6 below.
- **T, horizontal: optical center.** The T sits in the panel left of the perforation: x 0..44, center
  22. A slanted T has empty triangles at the upper left and the lower right, so its box center is
  not where the eye puts its middle. The T is placed by its center of mass, the centroid of its
  area. With the bottom-left bar corner at x = a, the centroid is at a + 17.41: the bar (196 units²)
  and the stem rectangle (98) center at a + 17.5, and the triangle of the cut (24.5) at a + 16.33.
  a = 5 puts the centroid at 22.41, 0.41 right of the panel center. a = 4 would put it 0.59 left.
  So the T runs x 5..40: the space is 5 to the panel edge and 4 to the perforation.
- **Stem.** The middle line of the bar (y 9.5) runs x 8.5..36.5, center 22.5. The stem is 7 wide, so
  it runs 22.5 ± 3.5 = 19..26.
- **Notches and T.** The bar corner nearest a notch is (5, 13). Its distance to the notch center
  (0, 20) is √74 = 8.6, more than the notch radius 5, so the notch does not touch the T.
- **Stub.** The stub is x 46..64 (18 wide). Its notch takes x 59..64 at the middle.

The source path is in `logo-mark.svg`:

```
M4 0H60A4 4 0 0 1 64 4V15A5 5 0 0 0 64 25V36A4 4 0 0 1 60 40H4A4 4 0 0 1 0 36V25A5 5 0 0 0 0 15V4A4 4 0 0 1 4 0Z
M12 6H40L33 13H26V27L19 34V13H5Z
M44 4A1 1 0 0 1 46 4V6A1 1 0 0 1 44 6Z ... (one subpath per slot, step 6 in y)
```

### Measured check

A 512 × 320 render of `logo-mark.svg` (scale 8) gives these results. G is a gold pixel and . is a
hole pixel.

- The hole area in the left panel is 20384 px, which is 318.5 units². Its centroid is at
  x = 179.28 px (22.41 units) and y = 114.05 px (14.26 units). The panel center is x = 176 px.
- Row y = 76 (the middle line of the bar): `G68 .223 G62 .14 G145`. The bar starts 68 px from the
  left edge and ends 62 px from the perforation.
- Row y = 160 (the ticket middle, through the stem): `.40 G112 .56 G264 .40`. The stem is at
  x 152..208 (19..26 units).
- Column x = 360 (the perforation): `G24 .32 G16 .32 G16 .32 G16 .32 G16 .32 G16 .32 G24`. All 6
  slots are 32 px, all gaps are 16 px, and the two ends are 24 px.

## Construction of the favicon

`favicon.svg` uses its own grid, `viewBox="0 0 32 32"`. It has fewer slots and a simpler T so that
it stays legible at 16 px. The T uses s = 4, but its bar is 3s long, not 4s: a 5s-wide T leaves no
space to the slots at this size. Every vertical and horizontal edge of the T and the slots is on an
even coordinate, so it falls on a whole pixel at 16 px. Only the 45-degree cuts are soft.

| Part | Geometry |
|---|---|
| Ticket body | Rect x 0..32, y 4..28. Corner radius 3. The body center is y = 16. |
| Notches | Radius 3. The centers are (0, 16) and (32, 16). |
| Perforation | 3 slots, 2 wide and 4 tall, round ends, at x 22..24. The gap is 4. |
| T bar | Parallelogram, y 8..12. Top edge x 6..18, bottom edge x 2..14. |
| T stem | x 8..12, from y 12. The left side ends at y 24, the right side at y 20. The cut runs from (12, 20) to (8, 24). |

- Perforation: 3 × 4 + 2 × 4 = 20. It starts at 16 − 10 = 6. The slots are at y 6, 14 and 22.
- T vertical: the T is 16 tall, at 16 ± 8 = 8..24. The space to the body is 4 above and 4 below.
- T horizontal: the panel is x 0..22, center 11. The centroid of the T is at a + 7.94, with the
  bottom-left bar corner at x = a. a = 3 would center it, but then the stem edges fall on half
  pixels at 16 px. a = 2 keeps them sharp and puts the centroid at 9.94, half a pixel left of the
  center at 16 px.
- Measured at 512 px (scale 16), the row through the middle line of the bar and a slot (y = 152)
  is `G72 .191 G91 .28 G130`. The column through the stem (x = 160) is `.64 G64 .223 G97 .64`.

The favicon is Gold Deep by default. A `prefers-color-scheme: dark` rule in the SVG changes it to
Gold. The PNG exports use Gold Deep.

## Wordmark and lockup

The wordmark is "Telesphorepo" in Geist SemiBold, tracking −0.02 em, with kerning on. The glyphs
are outlined to paths, so no font is necessary. The cap height is 20 units (half the ticket
height), which is a font size of 28.17. The viewBox is the ink box, `0 0 174.34 24.23`. The glyph
coordinates are rounded to 2 decimal places. They are the only fractional coordinates in the flat
files.

The horizontal lockup is `0 0 254.34 40`:

- The mark is at x 0..64, y 0..40.
- The gap from the mark to the wordmark ink is 16 units.
- The wordmark cap height is centered on the ticket middle: the cap top is at y 10, the baseline is
  at y 30. The descender of "p" ends above y 40.

## Felt ticket

`ticket-skeuomorphic.svg` is a 720 × 480 canvas. The ticket is the flat geometry × 10 (640 × 400)
at offset (40, 32), and the margin holds the drop shadow. It is pure SVG with filters. It has no
external images and no fonts. It renders the same in Chromium (checked in headless Brave) and in
librsvg (sharp).

The ticket is matte wool felt: there is no gradient, no sheen and no specular light. All the
texture comes from `feTurbulence` noise in user units, so it keeps its size at every export scale.
The layers, from bottom to top:

1. A soft drop shadow (blur 16, offset 18) and a contact shadow (blur 3, offset 3).
2. The felt, in one filter:
   - The cut edge: the outline displaced by fine noise (scale 2.6) and blurred a little, so the
     edge is soft and uneven. A thin halo of loose fibers (low alpha) sits outside it.
   - The surface: fine noise and a slow mottle make a height map. `feDiffuseLighting` lights it
     from the top left (azimuth 225, elevation 62). Diffuse light only, so the felt stays matte.
   - The fibers: two layers of short, curled hairs from the ridges of `turbulence` noise, one light
     and one dark, both at low alpha.
   - A soft darker band along every edge, where the cut felt thins.
3. Rims around the punched slots: a soft, compressed ring. The slots are real holes.
4. Printed ink: an inset border on each panel, which follows the notch, "ADMIT ONE" above the T,
   "TELESPHOREPO" below it, and a serial "No 000001" on the stub. The text is Geist SemiBold in
   uppercase with wide letter-spacing, outlined to paths. The ink filter moves the ink along the
   fibers, softens it, and varies its density, so it looks printed on felt.
5. The pressed T: a soft impression, not a bevel. The floor is darker. A blurred inner shadow lies
   along the top and left walls and a soft light along the bottom and right walls. Around the T,
   the felt rises a little: shade on the upper left, light on the lower right.

`og-image.svg` and `og-image.png` are 1200 × 630. They show the felt ticket on Carbon with a soft
radial light behind it, and the wordmark in Paper to its right. The ticket is at (78, 107). The
wordmark cap height is 40, centered on the ticket middle. The gap from the ticket to the wordmark
is 56.

## Avatar

`avatar.svg` is the flat mark on a square, for a profile picture. Its viewBox is `0 0 512 512`,
filled with Carbon, and the mark is at x 80..432, y 146..366: the 64 × 40 grid at scale 5.5, 352
wide (a whole multiple of 16). The corners of the ticket are 207.5 from the center, 81% of the
radius of the inscribed circle, so a round crop keeps the whole ticket with room around it.
`avatar-on-light.svg` is the same on Paper with Gold Deep. The T and the slots stay holes, so the
square's color shows through them. The PNGs are rendered with sharp at 256, 512 and 1024.
## Clear space and minimum sizes

- **Clear space:** keep 8 units (one fifth of the ticket height) clear on all sides of the mark and
  the lockup. At 64 px wide, that is 8 px.
- **Flat mark:** minimum 32 px wide. Below that, use the favicon.
- **Favicon:** legible at 16 px.
- **Lockup:** minimum 24 px tall.
- **Felt ticket:** minimum 320 px wide. Below that, the fibers and the printed text turn into
  noise. Use the flat mark.

## Which file to use

| Use | File |
|---|---|
| Browser tab, PWA icon | `favicon.svg`, `png/favicon-{16,32,64,128,256,512}.png` |
| App header on a dark bar | `logo-mark.svg` or `lockup-horizontal-on-dark.svg` |
| App header on a light bar | `logo-mark-on-light.svg` or `lockup-horizontal.svg` |
| Inline in UI that sets its own color | `logo-mark-mono.svg` (`currentColor`) |
| Name only | `wordmark.svg` (Carbon), `wordmark-on-dark.svg` (Paper) |
| Raster mark | `png/logo-mark-*.png`, `png/logo-mark-on-light-*.png` (16 to 512 wide) |
| Avatar (square: GitHub, Discord, social profiles) | `avatar.svg` (Carbon), `avatar-on-light.svg` (Paper); `png/avatar-{256,512,1024}.png`, `png/avatar-on-light-{256,512,1024}.png` |
| Raster lockup | `png/lockup-horizontal@2x.png`, `@4x`, and the `-on-dark` pair (80 and 160 px tall) |
| Website hero, sign-in page, empty states | `ticket-skeuomorphic.svg` (the felt ticket), `png/ticket-skeuomorphic.png` (720), `@2x` (1440) |
| Social card | `png/og-image.png` (source `og-image.svg`) |

The rule: use the flat mark at 32 px and below. Use the felt ticket only for large hero uses.

## Do and don't

Do:

- Put the mark on Carbon, on white, or on a plain photo area with enough contrast.
- Use Gold Deep on a light background.
- Keep the T and the slots as holes, so the background shows through.
- Scale the flat mark in whole multiples of 16 px wide where you can, so the straight edges stay
  sharp.

Don't:

- Don't fill the T or the slots with a color. They are holes.
- Don't stand the T upright, move it off its optical center, or change the number of slots in the
  mark.
- Don't stretch, rotate or skew the mark, or add an outline or a glow to it.
- Don't use the felt ticket at small sizes, or next to the flat mark in the same view.
- Don't put Gold on white.
- Don't set the wordmark in a different typeface, or retype it in live text for a logo.
