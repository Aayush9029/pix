<h1 align="center">pix</h1>
<p align="center">OpenAI image generation for the terminal — streaming, parallel, stdin-friendly.</p>

<p align="center">
  <a href="https://github.com/Aayush9029/pix/releases/latest"><img src="https://img.shields.io/github/v/release/Aayush9029/pix" alt="Release"></a>
  <a href="https://github.com/Aayush9029/pix/blob/main/LICENSE"><img src="https://img.shields.io/github/license/Aayush9029/pix" alt="License"></a>
</p>

## Install

```bash
brew install aayush9029/tap/pix
```

Or tap first:

```bash
brew tap aayush9029/tap
brew install pix
```

Requires `OPENAI_API_KEY` in your environment. Defaults to `gpt-image-1.5`.

## Usage

```bash
pix "a corgi astronaut on mars, cinematic"
pix -n 4 --progress "isometric tiny village"
echo "neon koi fish" | pix -s 1536x1024 -q high
pix -b "studio ghibli style" -p "rainy train station"
pix -m dall-e-3 -q hd -s 1792x1024 "art deco poster of saturn"
```

Images save as `<sanitized-prompt>-<timestamp>[-vN].<ext>` in the current directory (or `-o <dir>`).

## Options

| Option | Description |
|--------|-------------|
| `-m, --model <id>` | `gpt-image-1.5` (default), `gpt-image-1`, `gpt-image-1-mini`, `dall-e-3`, `dall-e-2` |
| `-p, --prompt <text>` | Prompt text (overrides positional / stdin) |
| `-b, --base <text>` | Base prompt prepended to the main prompt |
| `-n <1-10>` | Number of variants generated in parallel |
| `-s, --size <wxh>` | `1024x1024`, `1536x1024`, `1024x1536`, `auto` |
| `-q, --quality <level>` | `auto` / `low` / `medium` / `high` (gpt-image), `standard` / `hd` (dall-e-3) |
| `-f, --format <ext>` | `png` / `jpeg` / `webp` (gpt-image only) |
| `--background <v>` | `auto` / `transparent` / `opaque` |
| `--compression <0-100>` | Compression for `jpeg` / `webp` |
| `--stream` | Stream via SSE (defaults to `--partials 2`) |
| `--partials <0-3>` | Number of partial frames to save as the image renders |
| `--progress` | Shortcut for `--stream --partials 3` |
| `-o, --output <dir>` | Output directory (default `.`) |
| `--name <prefix>` | Filename prefix override |
| `--json` | Emit a JSON summary |

## How it works

1. Resolves prompt from `--prompt`, positional args, or stdin.
2. Prepends `--base` if set, validates model constraints.
3. Spawns `n` parallel calls to `POST /v1/images/generations`.
4. With `--stream` / `--partials` / `--progress`, parses Server-Sent Events and writes each partial frame (`…-p1.png`, `…-p2.png`, …) plus the final image as they arrive.
5. Base64 decodes each image and writes it to disk with a timestamped filename.

## Requirements

macOS. `OPENAI_API_KEY` exported in your shell. Streaming and `output_format` are only supported on `gpt-image-*` models; `dall-e-2` / `dall-e-3` fall back to a non-streaming b64 response.

## License

MIT
