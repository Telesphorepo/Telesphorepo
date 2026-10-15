# Telesphorepo brand

The mark is a gold admission ticket. A letter T and a column of perforation slots are punched
through it, and a semicircular notch is cut into each short edge. The brand has two tiers:

- The **flat mark** is for small and functional sizes: the favicon, the app header, docs and
  lockups. Use it at 32 px and below without exception.
- The **skeuomorphic ticket** is for large hero uses: the website hero, the sign-in page,
  empty states and the social (OG) image. It has the same geometry as the flat mark, at 10x.

## Colors

| Name | Hex | Use |
|---|---|---|
| Gold | `#E9B546` | The mark on dark backgrounds. 9.18:1 on Carbon. |
| Gold Deep | `#A8760F` | The mark on light backgrounds and in the favicon. 3.99:1 on white. |
| Carbon | `#131838` | The brand background. The wordmark on light backgrounds. |
| Paper | `#F5F1E6` | The wordmark on Carbon. |

Gold is 1.88:1 on white, which is too low for a graphic. On a light background, use Gold Deep.

The skeuomorphic ticket also uses these internal tones. Do not use them as brand colors.

- Foil gradient: `#FBE3A0`, `#EEC05A`, `#CF972E`, `#F4D27A`, `#DDA840`, `#B27C20`.
- Debossed T: `#B98325` to `#CB9636`. Printed ink: `#6E4708`.
- Shadow: `#05071A`.

## Construction of the flat mark

All shapes are on an integer grid in `viewBox="0 0 64 40"`. The file is one `<path>` with
`fill-rule="evenodd"`. The T and the slots are subpaths inside the outline, so they are real
holes: the background shows through. There are no strokes and no transforms.

| Part | Geometry |
|---|---|
| Ticket body | Rect x 0..64, y 0..40. Corner radius 4. |
| Notches | Two circles of radius 5. The centers are (0, 20) and (64, 20), on the edges at the vertical middle. |
| Perforation | 6 slots, each 2 wide and 4 tall, with fully round ends (radius 1). The slots are at x 44..46 (center x = 45). The gap between slots is 2. |
| T bar | x 10..34, y 8..16 (24 wide, 8 tall). |
| T stem | x 18..26, y 16..32 (8 wide, 16 tall). |

The arithmetic:

- **Perforation, vertical.** The column height is 6 × 4 + 5 × 2 = 34. It starts at (40 − 34) / 2 = 3
  and ends at 37. The column center is (3 + 37) / 2 = 20, the ticket middle.
  The slots are at y 3, 9, 15, 21, 27 and 33.
- **T, horizontal.** The T sits in the panel left of the perforation: x 0..44. The panel center is
  44 / 2 = 22. The bar is 24 wide, so it runs 22 − 12 = 10 to 22 + 12 = 34. The stem is 8 wide, so it runs
  22 − 4 = 18 to 22 + 4 = 26. The space is 10 on each side of the bar (0..10 and 34..44).
- **T, vertical.** The T is 8 + 16 = 24 tall. It starts at (40 − 24) / 2 = 8 and ends at 32. The space
  is 8 above and 8 below.
- **Notches and T.** The notch top is at y = 20 − 5 = 15. The bar bottom is at 16, and the bar is 10
  from the edge, so the notch does not touch the T.
- **Stub.** The stub is x 46..64 (18 wide). Its notch takes x 59..64 at the middle.

The source path is in `logo-mark.svg`:

```
M4 0H60A4 4 0 0 1 64 4V15A5 5 0 0 0 64 25V36A4 4 0 0 1 60 40H4A4 4 0 0 1 0 36V25A5 5 0 0 0 0 15V4A4 4 0 0 1 4 0Z
M10 8H34V16H26V32H18V16H10Z
M44 4A1 1 0 0 1 46 4V6A1 1 0 0 1 44 6Z ... (one subpath per slot, step 6 in y)
```

### Measured check

A 512 × 320 render of `logo-mark.svg` (scale 8) gives these runs of gold (G) and hole (.) pixels:

- Row y = 96 (through the T bar and a slot): `G80 .192 G80 .16 G144`. The space is 80 px left of
  the T and 80 px right of the T.
- Column x = 176 (the T center): `G64 .192 G64`. The space is 64 px above and 64 px below.
- Column x = 360 (the perforation): `G24 .32 G16 .32 G16 .32 G16 .32 G16 .32 G16 .32 G24`. All 6
  slots are 32 px, all gaps are 16 px, and the two ends are 24 px.
- The notches are mirror images: the left and right 48 px edge strips differ in 2 pixels of
  anti-aliasing.

## Construction of the favicon

`favicon.svg` uses its own grid, `viewBox="0 0 32 32"`. It has fewer slots and a thicker T so that
it stays legible at 16 px. All the coordinates are even, except the radius-3 corners and notches, so
every edge of the T and the slots falls on a whole pixel at 16 px.

| Part | Geometry |
|---|---|
| Ticket body | Rect x 0..32, y 4..28. Corner radius 3. The body center is y = 16. |
| Notches | Radius 3. The centers are (0, 16) and (32, 16). |
| Perforation | 3 slots, 2 wide and 4 tall, round ends, at x 22..24. The gap is 4. |
| T bar | x 4..18, y 8..14 (14 wide, 6 tall). |
| T stem | x 8..14, y 14..24 (6 wide, 10 tall). |

- Perforation: 3 × 4 + 2 × 4 = 20. It starts at 16 − 10 = 6. The slots are at y 6, 14 and 22.
- T horizontal: the panel is x 0..22, center 11. The bar is 11 ± 7 = 4..18. The stem is 11 ± 3 = 8..14.
  The space is 4 on each side.
- T vertical: the T is 16 tall, at 16 ± 8 = 8..24. The space to the body is 4 above and 4 below.
- Measured at 512 px (scale 16), the row through the bar and a slot is `G64 .224 G64 .32 G128`:
  64 px left and 64 px right of the T. The T column is `.64 G64 .256 G64 .64`.

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
- The gap from the mark to the wordmark ink is 16 units (the T bar width minus the stem width).
- The wordmark cap height is centered on the ticket middle: the cap top is at y 10, the baseline is
  at y 30. The descender of "p" ends above y 40.

## Skeuomorphic ticket

`ticket-skeuomorphic.svg` is a 720 × 480 canvas. The ticket is the flat geometry × 10 (640 × 400)
at offset (40, 32), and the margin holds the drop shadow. It is pure SVG with filters. It has no
external images and no fonts. It renders the same in Chromium and Firefox (checked in headless
Brave and Firefox) and in librsvg (sharp).

The layers, from bottom to top:

1. A soft drop shadow (blur 16, offset 18) and a contact shadow (blur 2.5, offset 3).
2. The gold foil: a diagonal linear gradient across the ticket.
3. Paper fibers: horizontal `feTurbulence` streaks at low opacity.
4. A specular sheen: a soft diagonal light band.
5. Printed ink: an inset border on each panel, which follows the notch, "ADMIT ONE" above the T,
   "TELESPHOREPO" below it, and a serial "No 000001" on the stub. The text is Geist SemiBold in
   uppercase with wide letter-spacing, outlined to paths. A noise mask makes the ink look printed.
6. The debossed T: a darker fill with an inner shadow at the top left, an inner highlight at the
   bottom right, and a light lip below the edges.
7. Rims around the punched slots. The slots are real holes.
8. Paper grain: fine `feTurbulence` speckle.
9. The card edge: a dark line along the bottom edge and a light line along the top edge.

`og-image.svg` and `og-image.png` are 1200 × 630. They show the ticket on Carbon with a soft
radial light behind it, and the wordmark in Paper to its right. The wordmark cap height is 40,
centered on the ticket middle. The gap from the ticket to the wordmark is 56.

## Clear space and minimum sizes

- **Clear space:** keep 8 units (one fifth of the ticket height) clear on all sides of the mark and
  the lockup. At 64 px wide, that is 8 px.
- **Flat mark:** minimum 32 px wide. Below that, use the favicon.
- **Favicon:** legible at 16 px.
- **Lockup:** minimum 24 px tall.
- **Skeuomorphic ticket:** minimum 320 px wide. Below that, the grain and the printed text turn
  into noise. Use the flat mark.

## Which file to use

| Use | File |
|---|---|
| Browser tab, PWA icon | `favicon.svg`, `png/favicon-{16,32,64,128,256,512}.png` |
| App header on a dark bar | `logo-mark.svg` or `lockup-horizontal-on-dark.svg` |
| App header on a light bar | `logo-mark-on-light.svg` or `lockup-horizontal.svg` |
| Inline in UI that sets its own color | `logo-mark-mono.svg` (`currentColor`) |
| Name only | `wordmark.svg` (Carbon), `wordmark-on-dark.svg` (Paper) |
| Raster mark | `png/logo-mark-*.png`, `png/logo-mark-on-light-*.png` (16 to 512 wide) |
| Raster lockup | `png/lockup-horizontal@2x.png`, `@4x`, and the `-on-dark` pair (80 and 160 px tall) |
| Website hero, sign-in page, empty states | `ticket-skeuomorphic.svg`, `png/ticket-skeuomorphic.png` (720), `@2x` (1440) |
| Social card | `png/og-image.png` (source `og-image.svg`) |

The rule: use the flat mark at 32 px and below. Use the skeuomorphic ticket only for large hero
uses.

## Do and don't

Do:

- Put the mark on Carbon, on white, or on a plain photo area with enough contrast.
- Use Gold Deep on a light background.
- Keep the T and the slots as holes, so the background shows through.
- Scale the flat mark in whole multiples of 16 px wide where you can, so the edges stay sharp.

Don't:

- Don't fill the T or the slots with a color. They are holes.
- Don't move the T off its computed center, or change the number of slots in the mark.
- Don't stretch, rotate or skew the mark, or add an outline or a glow to it.
- Don't use the skeuomorphic ticket at small sizes, or next to the flat mark in the same view.
- Don't put Gold on white.
- Don't set the wordmark in a different typeface, or retype it in live text for a logo.
