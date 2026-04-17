package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Aayush9029/pix/internal/api"
	"github.com/Aayush9029/pix/internal/config"
	"github.com/Aayush9029/pix/internal/ui"
	"github.com/spf13/pflag"
)

var version = "dev"

const (
	defaultModel = "gpt-image-1.5"
	// OpenAI caps partial_images at 3 across all image endpoints as of 2026-04.
	// We always request the max — the whole point of streaming is to get every
	// intermediate frame the server is willing to send.
	maxPartials = 3
)

var supportedModels = map[string]bool{
	"gpt-image-1.5":    true,
	"gpt-image-1":      true,
	"gpt-image-1-mini": true,
}

type options struct {
	model       string
	prompt      string
	base        string
	images      []string
	n           int
	size        string
	quality     string
	format      string
	compression int
	transparent bool
	output      string
	jsonOut     bool
	showVersion bool
	showHelp    bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		ui.Fatalf("%s", err)
	}
}

func run(args []string) error {
	opts, positional, err := parseFlags(args)
	if err != nil {
		return err
	}
	if opts.showVersion {
		fmt.Printf("pix %s\n", version)
		return nil
	}
	if opts.showHelp {
		showHelp()
		return nil
	}

	if !supportedModels[opts.model] {
		return fmt.Errorf("unsupported model %q (use gpt-image-1.5, gpt-image-1, or gpt-image-1-mini)", opts.model)
	}

	prompt, err := resolvePrompt(opts, positional)
	if err != nil {
		return err
	}
	if opts.base != "" {
		prompt = strings.TrimSpace(opts.base) + "\n\n" + prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("no prompt provided (pass as arg, --prompt, or pipe via stdin)")
	}

	apiKey := config.APIKey()
	if apiKey == "" {
		return fmt.Errorf("%s not set in environment", config.EnvAPIKey)
	}

	for _, p := range opts.images {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("image %s: %w", p, err)
		}
	}
	if len(opts.images) > 16 {
		return fmt.Errorf("--image accepts up to 16 entries (got %d)", len(opts.images))
	}

	size, err := translateSize(opts.size)
	if err != nil {
		return err
	}

	outDir, err := config.ResolveOutputDir(opts.output)
	if err != nil {
		return err
	}

	format := opts.format
	if format == "" {
		format = "png"
	}
	if opts.transparent && format == "jpeg" {
		return errors.New("--transparent requires format png or webp")
	}

	partials := maxPartials
	req := api.Request{
		Model:         opts.model,
		Prompt:        prompt,
		N:             1,
		Size:          size,
		Quality:       opts.quality,
		OutputFormat:  format,
		Images:        opts.images,
		Stream:        true,
		PartialImages: &partials,
	}
	if opts.transparent {
		req.Background = "transparent"
	}
	if opts.compression >= 0 && opts.compression <= 100 && (format == "jpeg" || format == "webp") {
		c := opts.compression
		req.OutputCompression = &c
	}

	stamp := time.Now().Format("20060102-150405")
	baseName := sanitizeName(prompt)

	client := api.NewClient(apiKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mode := "generate"
	if len(opts.images) > 0 {
		mode = fmt.Sprintf("edit ×%d", len(opts.images))
	}

	// Pre-compute every output path so we can show them up front — users know
	// exactly which files to `open` and watch update in place.
	paths := make([]string, opts.n)
	for i := 0; i < opts.n; i++ {
		name := baseName + "-" + stamp
		if opts.n > 1 {
			name = fmt.Sprintf("%s-v%d", name, i+1)
		}
		paths[i] = filepath.Join(outDir, name+"."+format)
	}

	if !opts.jsonOut {
		ui.Header(fmt.Sprintf("pix · %s · %s · %dx", opts.model, mode, opts.n))
		if ui.IsTTY() {
			ui.Dimf("  prompt: %s", truncate(prompt, 80))
			if opts.size != "" && opts.size != "auto" {
				ui.Dimf("  size: %s", opts.size)
			}
			if opts.quality != "" {
				ui.Dimf("  quality: %s", opts.quality)
			}
			for _, p := range paths {
				ui.Dimf("  → %s  (updates live)", relPath(p))
			}
			fmt.Println()
		}
	}

	type variantResult struct {
		path  string
		usage *api.Usage
		err   error
	}

	results := make([]variantResult, opts.n)
	var wg sync.WaitGroup
	var printMu sync.Mutex

	start := time.Now()

	for i := 0; i < opts.n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tag := ""
			if opts.n > 1 {
				tag = fmt.Sprintf("[v%d] ", idx+1)
			}
			path := paths[idx]

			var usage *api.Usage
			err := client.GenerateStream(ctx, req, func(ev api.StreamEvent) error {
				// Every event that carries image data — partial or final — is
				// written to the same canonical path atomically. Openers see
				// the file progressively improve; there's exactly one file
				// per variant at the end, never a `-p1` / `-p2` mess.
				if ev.B64JSON == "" {
					return nil
				}
				if err := writeAtomic(path, ev.B64JSON); err != nil {
					return err
				}
				switch ev.Kind {
				case api.EventPartial:
					if !opts.jsonOut {
						printMu.Lock()
						ui.Dimf("  %sframe %d/%d", tag, ev.PartialImageIndex+1, partials)
						printMu.Unlock()
					}
				case api.EventCompleted:
					usage = ev.Usage
					if !opts.jsonOut {
						printMu.Lock()
						ui.Success(tag + "done → " + relPath(path))
						printMu.Unlock()
					}
				}
				return nil
			})
			results[idx] = variantResult{path: path, usage: usage, err: err}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	var firstErr error
	var saved []string
	totalTokens := 0
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		if r.err == nil {
			saved = append(saved, r.path)
		}
		if r.usage != nil {
			totalTokens += r.usage.TotalTokens
		}
	}

	if opts.jsonOut {
		out := map[string]any{
			"model":        opts.model,
			"mode":         mode,
			"prompt":       prompt,
			"saved":        saved,
			"elapsed_ms":   elapsed.Milliseconds(),
			"total_tokens": totalTokens,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else if ui.IsTTY() {
		fmt.Println()
		ui.Dimf("%d saved · %s · %d tokens", len(saved), elapsed.Round(100*time.Millisecond), totalTokens)
	}

	return firstErr
}

// --- prompt resolution ---

func resolvePrompt(opts options, positional []string) (string, error) {
	if opts.prompt != "" {
		return opts.prompt, nil
	}
	if len(positional) > 0 {
		return strings.Join(positional, " "), nil
	}
	fi, err := os.Stdin.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		buf, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(string(buf)), nil
	}
	return "", nil
}

// --- size enum ---

func translateSize(input string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "auto":
		return "auto", nil
	case "square":
		return "1024x1024", nil
	case "landscape":
		return "1536x1024", nil
	case "portrait":
		return "1024x1536", nil
	}
	return "", fmt.Errorf("invalid --size %q (use square, landscape, portrait, or auto)", input)
}

// --- flag parsing ---

func parseFlags(args []string) (options, []string, error) {
	var opts options
	fs := pflag.NewFlagSet("pix", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVarP(&opts.model, "model", "m", defaultModel, "")
	fs.StringVarP(&opts.prompt, "prompt", "p", "", "")
	fs.StringVarP(&opts.base, "base", "b", "", "")
	fs.StringArrayVarP(&opts.images, "image", "i", nil, "")
	fs.IntVarP(&opts.n, "n", "n", 1, "")
	fs.StringVarP(&opts.size, "size", "s", "auto", "")
	fs.StringVarP(&opts.quality, "quality", "q", "", "")
	fs.StringVarP(&opts.format, "format", "f", "", "")
	fs.IntVar(&opts.compression, "compression", -1, "")
	fs.BoolVar(&opts.transparent, "transparent", false, "")
	fs.StringVarP(&opts.output, "output", "o", ".", "")
	fs.BoolVar(&opts.jsonOut, "json", false, "")
	fs.BoolVarP(&opts.showVersion, "version", "v", false, "")
	fs.BoolVarP(&opts.showHelp, "help", "h", false, "")

	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
	if opts.n < 1 || opts.n > 10 {
		return opts, nil, fmt.Errorf("--n must be between 1 and 10 (got %d)", opts.n)
	}
	return opts, fs.Args(), nil
}

// --- helpers ---

var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 40 {
		s = s[:40]
	}
	s = nameSanitizer.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	s = strings.ToLower(s)
	if s == "" {
		s = "pix"
	}
	return s
}

// writeAtomic decodes a base64 image and writes it to `path`, replacing any
// previous contents atomically. Partial frames arriving while a viewer has
// the file open never leave a torn/partial byte stream on disk.
func writeAtomic(path, b64Data string) error {
	raw, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func relPath(p string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(cwd, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func showHelp() {
	fmt.Printf(`%spix%s v%s — OpenAI image generation & editing for the terminal

%sUsage:%s
  pix <prompt>                         Generate from a prompt
  pix --prompt <prompt>                Same, as a flag
  echo "prompt" | pix                  Read prompt from stdin
  pix -i photo.png "make it cyberpunk" Edit an existing image (image-to-image)
  pix -i a.png -i b.png "combine them" Reference multiple input images

Streams by default — the output file is overwritten atomically each time
the API emits a partial frame, so viewers see it improve live.

%sOptions:%s
  -m, --model <id>        gpt-image-1.5 (default) | gpt-image-1 | gpt-image-1-mini
  -p, --prompt <text>     Prompt text (overrides positional / stdin)
  -b, --base <text>       Base prompt prepended to the main prompt
  -i, --image <path>      Input image for editing (repeatable, up to 16)
  -n <1-10>               Variants generated in parallel
  -s, --size <preset>     square | landscape | portrait | auto
  -q, --quality <level>   auto | low | medium | high
  -f, --format <ext>      png | jpeg | webp (default: png)
      --compression <n>   0-100 for jpeg / webp
      --transparent       Transparent background (png / webp only)
  -o, --output <dir>      Output directory (default: .)
      --json              Emit a JSON summary to stdout
  -v, --version           Show version
  -h, --help              Show this help

%sExamples:%s
  pix "a corgi astronaut on mars, cinematic"
  pix -n 4 "isometric tiny village"
  echo "neon koi fish" | pix -s landscape -q high
  pix -b "studio ghibli style" -p "rainy train station"
  pix -i cat.png --transparent "remove background, add sparkles"

%sRequires OPENAI_API_KEY in the environment.%s
`, ui.Bold, ui.Reset, version,
		ui.Bold, ui.Reset,
		ui.Bold, ui.Reset,
		ui.Bold, ui.Reset,
		ui.Dim, ui.Reset)
}
