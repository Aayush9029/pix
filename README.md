<p align="center">
  <img src="assets/icon.png" width="128" alt="pix">
</p>
<h1 align="center">pix</h1>
<p align="center">OpenAI image generation & editing for the terminal — streaming, parallel, image-to-image.</p>

<p align="center">
  <a href="https://github.com/Aayush9029/pix/releases/latest"><img src="https://img.shields.io/github/v/release/Aayush9029/pix" alt="Release"></a>
  <a href="https://github.com/Aayush9029/pix/blob/main/LICENSE"><img src="https://img.shields.io/github/license/Aayush9029/pix" alt="License"></a>
</p>

## Install

```bash
brew install aayush9029/tap/pix
```

Requires `OPENAI_API_KEY` in your environment. Defaults to `gpt-image-1.5`.

## Usage

```bash
pix "a corgi astronaut on mars, cinematic"
pix -n 4 --progress "isometric tiny village"
echo "neon koi fish" | pix -s landscape -q high
pix -b "studio ghibli style" -p "rainy train station"
pix -i photo.png "make it cyberpunk, neon rain"
pix -i a.png -i b.png --transparent "merge them into a single sticker"
```

Images save as `<sanitized-prompt>-<timestamp>[-vN].<ext>` in the current directory (or `-o <dir>`).

## Options

| Option | Description |
|--------|-------------|
| `-m, --model <id>` | `gpt-image-1.5` (default), `gpt-image-1`, `gpt-image-1-mini` |
| `-p, --prompt <text>` | Prompt text (overrides positional / stdin) |
| `-b, --base <text>` | Base prompt prepended to the main prompt |
| `-i, --image <path>` | Input image for editing (repeatable, up to 16) |
| `-n <1-10>` | Variants generated in parallel |
| `-s, --size <preset>` | `square` / `landscape` / `portrait` / `auto` |
| `-q, --quality <level>` | `auto` / `low` / `medium` / `high` |
| `-f, --format <ext>` | `png` / `jpeg` / `webp` |
| `--compression <0-100>` | Compression for `jpeg` / `webp` |
| `--transparent` | Transparent background (png / webp only) |
| `--stream` | Stream via SSE (defaults to `--partials 2`) |
| `--partials <0-3>` | Partial frames saved as the image renders |
| `--progress` | Shortcut for `--stream --partials 3` |
| `-o, --output <dir>` | Output directory (default `.`) |
| `--json` | Emit a JSON summary |

## How it works

1. Resolves the prompt from `--prompt`, positional args, or stdin; optionally prepends `--base`.
2. Picks the endpoint: `POST /v1/images/generations` with no `-i`, `POST /v1/images/edits` (multipart) with one or more `-i` paths.
3. Fans out `n` parallel requests — each variant is an independent API call.
4. With streaming enabled, parses Server-Sent Events and writes each partial frame (`…-p1.png`, `…-p2.png`, …) plus the final image as they arrive.
5. Base64-decodes each image and writes it to disk with a timestamped filename.

## Requirements

macOS, `OPENAI_API_KEY` exported. Only `gpt-image-*` models are supported — all of them allow streaming, transparency, and image-to-image editing.

## License

MIT
