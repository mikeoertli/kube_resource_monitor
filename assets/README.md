# Brand assets

Two marks, sharing one palette and one visual idea: a bar whose length is usage
and whose color is how close that usage is to the limit constraining it. Every
color here is lifted from the severity scale in
[`internal/render/color.go`](../internal/render/color.go), so the marks and the
terminal output are the same system rather than two things that happen to sit
near each other.

| | Mark | Use it for |
| --- | --- | --- |
| <img src="png/icon-64.png" width="48" height="48"> | **`icon.svg`** — three bars at increasing fill | Favicon, app icon, menu bar, anywhere below 32px |
| <img src="png/logo-128.png" width="48" height="48"> | **`logo.svg`** — a workload bar with two pods indented beneath | README header, social cards, slides, anywhere above 32px |

The split is deliberate. `logo.svg` encodes the workload rollup that separates
`krm` from `kubectl top`, but the indent that carries that meaning needs roughly
32px to read. `icon.svg` gives up the hierarchy to stay legible at 16px, where
three bars of different length and color are still unambiguous.

## Files

```
icon.svg            primary mark, full color
icon-mono.svg       primary mark, single color
logo.svg            hero mark, full color
logo-mono.svg       hero mark, single color
favicon.ico         16 / 32 / 48 / 64 / 128 / 256, each frame natively rendered
krm.icns            macOS bundle icon, 16 through 512@2x
png/                rasterized PNGs at the sizes above
```

Prefer the SVGs. The PNGs exist for the places that will not take vector:
`favicon.ico` consumers, macOS bundles, GitHub social preview images, and Slack
or Discord app icons.

## Single-color variants

`icon-mono.svg` and `logo-mono.svg` draw everything with `currentColor`. Inline
them in HTML, or set `color` in CSS, and the whole mark follows:

```html
<span style="color: #f9fafb">
  <!-- contents of icon-mono.svg -->
</span>
```

Opened standalone they fall back to the dark ink set by the `color` attribute on
the root `<svg>`. Fill *length* still encodes severity in these variants, so
they stay meaningful with the hue removed — which is also why they work for
anyone who cannot distinguish red from green.

Pre-rendered mono PNGs come in both inks: `icon-mono-*.png` is dark ink for
light backgrounds, `icon-mono-white-*.png` is light ink for dark ones.

## Palette

| Token | Hex | Meaning |
| --- | --- | --- |
| normal | `#22c55e` | comfortable headroom |
| elevated | `#facc15` | worth watching |
| critical | `#f87171` | at or past the limit |
| surface | `#0f172a` | tile background |
| track | `#1e293b` | the unfilled part of a bar |
| connector | `#475569` | the tree lines in `logo.svg` |

## Regenerating

The PNGs, `.ico`, and `.icns` are all derived from the two SVGs. `krm.icns` is
rendered with a 9% inset, because macOS icons sit inside a safe area rather than
running edge to edge; everything else is edge to edge.

Each raster size is rendered natively rather than downsampled from one large
bitmap. That matters most at 16px, where resampling turns three 1.75px bars into
a smear.
