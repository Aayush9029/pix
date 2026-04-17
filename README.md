<p align="center">
  <img src="assets/icon.png" width="128" alt="pix">
</p>
<h1 align="center">pix</h1>
<p align="center">OpenAI image generation & editing for the terminal — live streaming, parallel, image-to-image.</p>

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

Images save as `<sanitized-prompt>-<timestamp>[-vN].<ext>` and **update in place** as partial frames stream in — `open` the file once and watch it sharpen.

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
| `-o, --output <dir>` | Output directory (default `.`) |
| `--json` | Emit a JSON summary |

## How it works

1. Resolves the prompt from `--prompt`, positional args, or stdin; optionally prepends `--base`.
2. Picks the endpoint: `POST /v1/images/generations` when there are no `-i` inputs, `POST /v1/images/edits` (multipart) otherwise.
3. Fans out `-n` parallel goroutines — each variant is an independent streaming API call with `partial_images=3` (the cap OpenAI exposes).
4. Every partial frame and the final image land at the same canonical filename via `tempfile + rename`, so the file stays valid at all times and viewers that watch file mtime see it refine.

## Requirements

macOS, `OPENAI_API_KEY` exported. Only `gpt-image-*` models are supported — all of them allow streaming, transparency, and image-to-image editing.

## License

MIT
