---
title: Transformations
description: Every query parameter the delivery route accepts, what it rejects, and what the engine can do that no URL reaches.
---

Everything here applies to `GET /api/v1/images/*`. The
[proxy](/falco/guides/proxy/) accepts a narrower subset.

## Two classes of parameter

The difference is deliberate and worth knowing before reading the tables:

- **Geometry and encoding** — `w`, `h`, `q`, `f`, `fit`. A malformed value is a
  **400**. Silently ignoring a typo would serve an image that is not the one
  asked for, which is worse than an error.
- **Everything else** — a malformed value **falls back to its default** and the
  request succeeds. The useful response is still the image.

## Size and framing

| Parameter | Aliases | Values | Default |
|---|---|---|---|
| `w` | `width` | 16 … the configured ceiling (2048) | Original width |
| `h` | `height` | 16 … the configured ceiling (2048) | Original height |
| `fit` | — | `cover`, `contain`, `fill` | `cover` |
| `gravity` | — | `center`, `north`, `south`, `east`, `west`, `northeast`, `northwest`, `southeast`, `southwest`, `smart`, `entropy` | `center` |

The ceiling is `processing.max_dimensions.width` / `.height`, and it is one of
the few settings with **no environment variable** — it comes from `config.yaml`
or from the compiled default of 2048.

Anything under 16 pixels is rejected: below that it is not a thumbnail, it is a
decode plus an encode for an image nobody can see. Above the configured ceiling
is rejected too — the ceiling exists so a URL cannot ask for a 30000-pixel
render.

`smart` and `entropy` are libvips' attention and entropy strategies: they pick
the crop window by looking at the image rather than by a fixed anchor.

## Encoding

| Parameter | Aliases | Values | Default |
|---|---|---|---|
| `q` | `quality` | 1 … 100 | `DEFAULT_QUALITY` (85) |
| `f` | `format` | `webp`, `jpeg`, `png`, `avif`, `heic` | Path extension, else `DEFAULT_FORMAT` (`webp`) |

The path extension is the format when `f` is absent: `/images/abc.webp`. As an
extension, `webp`, `jpg`, `jpeg`, `png` and `avif` are recognised — `heic` is
only reachable through `f=heic`. An unrecognised extension is a `400`, never a
guess.

## Cropping, rotation and flip

| Parameter | Values | Default |
|---|---|---|
| `crop_x`, `crop_y` | Non-negative pixels | 0 |
| `crop_w`, `crop_h` | Positive pixels, both required to crop | Unset |
| `rotate` | -360 … 360 degrees | 0 |
| `flip` | `horizontal`, `vertical` | Unset |

A manual crop is all-or-nothing: `crop_w` and `crop_h` are both required, and an
origin on its own is a `400`. Accepting half of it would crop from a corner the
caller never named.

Right angles — `90`, `180`, `270` and their negatives — use libvips' exact
rotation, which is a transpose: lossless, and exactly the size you expect. Any
other angle interpolates.

## Colour and effects

| Parameter | Range | Default |
|---|---|---|
| `brightness` | -100 … 100 | 0 |
| `contrast` | -100 … 100 | 0 |
| `gamma` | 0 … 3 | 0 (off — 1.0 is the identity) |
| `saturation` | -100 … 500 | 0. `-100` is greyscale, `100` doubles the chroma |
| `hue` | -180 … 180 degrees | 0 |
| `blur` | 0 … 100 | 0 |
| `sharpen` | 0 … 100 | 0 |

Saturation and hue are applied in LCh, where chroma and hue are their own bands,
so each is one multiply-and-add rather than a matrix over RGB.

A value outside these ranges is treated as malformed and falls back to "not
requested" rather than being clamped: a silently clamped value produces an image
that is not the one asked for, and gives the caller no way to notice.

## Trim and padding

| Parameter | Values | Default |
|---|---|---|
| `trim` | `1` to enable | Off |
| `trim_threshold` | 0 … 255 | 10 |
| `pad_top`, `pad_right`, `pad_bottom`, `pad_left` | Non-negative pixels | 0 |
| `pad_color` | Hex without `#`, e.g. `F5F5F5` | `FFFFFF` |

`trim` is enabled by the exact string `1`. `trim=true` does nothing — it is not
an error, it just leaves trimming off.

## EXIF and metadata

| Parameter | Values | Default |
|---|---|---|
| `orient` | `0` to disable EXIF auto-rotation | Auto-rotation **on** |
| `meta` | `1` to keep EXIF/ICC metadata | Metadata **stripped** |

Both of these opt *out* rather than in. The defaults are what you want almost
always: photos come out the right way up, and location data does not leak with
an avatar.

## Caching

| Parameter | Values | Default |
|---|---|---|
| `maxage` | Seconds | `CACHE_DEFAULT_MAX_AGE` |
| `smaxage` | Seconds | `CACHE_DEFAULT_SMAX_AGE` |

## Watermark

| Parameter | Values | Default |
|---|---|---|
| `wm` | Id of an image stored in Falco, optionally with a directory | Unset |
| `wm_url` | Absolute `http`/`https` URL, allowlisted | Unset |
| `wm_opacity` | 0 … 1 | 1 (opaque) |
| `wm_position` | `top-left`, `top-right`, `bottom-left`, `bottom-right`, `center` | `bottom-right` |
| `wm_scale` | 0 … 1, relative to the final image width | 0.2 |

```
?w=800&wm=logos/brand&wm_opacity=0.6&wm_position=bottom-right
```

`wm` and `wm_url` are mutually exclusive: naming both is a `400`, not a
precedence rule nobody would remember.

`wm` is read through the **same backend as the image itself**, so a scoped key
cannot reach a bucket it is not allowed to read by asking for a watermark out of
it.

`wm_url` requires `WATERMARK_ALLOWED_HOSTS`. With that variable unset, external
watermarks are refused with a `403`: the feature does not quietly degrade into
fetching from anywhere. The host is checked *before* the name is resolved — DNS
resolution is itself an outbound request — and the client will not dial a
private or reserved address even for an allowed host.

:::caution[A watermark that cannot be loaded is an error]
A missing id, a disallowed host, an unreachable URL, a file that is not an
image, or an overlay over 2 MB is reported as such (`404`, `403`, `502`,
`422`). None of them falls back to serving the image without its watermark,
because that response would be indistinguishable from one that worked.
:::

## The order the pipeline applies them

The order is load-bearing, not incidental:

1. **Orientation and geometry** — auto-orient, trim, crop, flip, rotate — so
   everything after works on the image as the viewer will see it.
2. **Resize**, so the expensive colour work runs on the smallest pixel count.
3. **Colour and effects.**
4. **Padding**, so the padding is not itself scaled or colour-shifted.
5. **The watermark**, last of all: its scale is relative to the width the viewer
   actually gets, and a logo that had gone through the colour adjustments would
   come out tinted by them.

## What no URL reaches

Nothing. Every transformation the pipeline implements is reachable from the
delivery route.

Older documentation listed `flop`. It was a second name for the same vips
operation as `flip=horizontal`, and it has been removed rather than given a URL
of its own.
