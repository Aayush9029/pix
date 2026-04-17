package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
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
)

var version = "dev"

const defaultModel = "gpt-image-1.5"

var supportedModels = map[string]bool{
	"gpt-image-1.5":     true,
	"gpt-image-1":       true,
	"gpt-image-1-mini":  true,
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type options struct {
	model         string
	prompt        string
	base          string
	images        stringList
	n             int
	size          string
	quality       string
	format        string
	compression   int
	transparent   bool
	stream        bool
	partials      int
	partialsSet   bool
	progress      bool
	output        string
	jsonOut       bool
	showVersion   bool
	showHelp      bool
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

	// Streaming behavior:
	//  --progress      → --stream --partials 3
	//  --stream alone  → --partials 2
	//  --partials N    → implies --stream
	streamRequested := opts.stream || opts.progress || opts.partialsSet
	partials := opts.partials
	if opts.progress {
		partials = 3
	} else if opts.stream && !opts.partialsSet {
		partials = 2
	}

	format := opts.format
	if format == "" {
		format = "png"
	}
	if opts.transparent && format == "jpeg" {
		return errors.New("--transparent requires format png or webp")
	}

	req := api.Request{
		Model:        opts.model,
		Prompt:       prompt,
		N:            1,
		Size:         size,
		Quality:      opts.quality,
		OutputFormat: format,
		Images:       []string(opts.images),
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
	ui.Header(fmt.Sprintf("pix · %s · %s · %dx", opts.model, mode, opts.n))
	if ui.IsTTY() {
		ui.Dimf("  prompt: %s", truncate(prompt, 80))
		if opts.size != "" {
			ui.Dimf("  size: %s", opts.size)
		}
		if opts.quality != "" {
			ui.Dimf("  quality: %s", opts.quality)
		}
		fmt.Println()
	}

	type variantResult struct {
		index   int
		savedAt string
		usage   *api.Usage
		err     error
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

			filename := baseName + "-" + stamp
			if opts.n > 1 {
				filename = fmt.Sprintf("%s-v%d", filename, idx+1)
			}

			if streamRequested {
				variantReq := req
				variantReq.Stream = true
				if partials > 0 {
					p := partials
					variantReq.PartialImages = &p
				}

				var savedPath string
				var usage *api.Usage

				err := client.GenerateStream(ctx, variantReq, func(ev api.StreamEvent) error {
					ext := ev.OutputFormat
					if ext == "" {
						ext = format
					}
					switch ev.Kind {
					case api.EventPartial:
						if ev.B64JSON == "" {
							return nil
						}
						path := filepath.Join(outDir, fmt.Sprintf("%s-p%d.%s", filename, ev.PartialImageIndex+1, ext))
						if err := writeB64(path, ev.B64JSON); err != nil {
							return err
						}
						printMu.Lock()
						ui.Status(fmt.Sprintf("%spartial %d → %s", tag, ev.PartialImageIndex+1, relPath(path)))
						printMu.Unlock()
					case api.EventCompleted:
						path := filepath.Join(outDir, filename+"."+ext)
						if err := writeB64(path, ev.B64JSON); err != nil {
							return err
						}
						savedPath = path
						usage = ev.Usage
					}
					return nil
				})
				results[idx] = variantResult{index: idx, savedAt: savedPath, usage: usage, err: err}
				if err == nil && savedPath != "" {
					printMu.Lock()
					ui.Success(tag + relPath(savedPath))
					printMu.Unlock()
				}
				return
			}

			// Non-streaming path.
			var spinner *ui.Spinner
			if ui.IsTTY() && opts.n == 1 {
				spinner = ui.NewSpinner("generating...")
				spinner.Start()
			}

			resp, err := client.Generate(ctx, req)
			if spinner != nil {
				spinner.Stop()
			}
			if err != nil {
				results[idx] = variantResult{index: idx, err: err}
				return
			}
			if len(resp.Data) == 0 || resp.Data[0].B64JSON == "" {
				results[idx] = variantResult{index: idx, err: errors.New("empty response")}
				return
			}
			ext := resp.OutputFormat
			if ext == "" {
				ext = format
			}
			path := filepath.Join(outDir, filename+"."+ext)
			if err := writeB64(path, resp.Data[0].B64JSON); err != nil {
				results[idx] = variantResult{index: idx, err: err}
				return
			}
			results[idx] = variantResult{index: idx, savedAt: path, usage: resp.Usage}
			printMu.Lock()
			ui.Success(tag + relPath(path))
			printMu.Unlock()
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
		if r.savedAt != "" {
			saved = append(saved, r.savedAt)
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
	fs := flag.NewFlagSet("pix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&opts.model, "model", defaultModel, "")
	fs.StringVar(&opts.model, "m", defaultModel, "")
	fs.StringVar(&opts.prompt, "prompt", "", "")
	fs.StringVar(&opts.prompt, "p", "", "")
	fs.StringVar(&opts.base, "base", "", "")
	fs.StringVar(&opts.base, "b", "", "")
	fs.Var(&opts.images, "image", "")
	fs.Var(&opts.images, "i", "")
	fs.IntVar(&opts.n, "n", 1, "")
	fs.StringVar(&opts.size, "size", "auto", "")
	fs.StringVar(&opts.size, "s", "auto", "")
	fs.StringVar(&opts.quality, "quality", "", "")
	fs.StringVar(&opts.quality, "q", "", "")
	fs.StringVar(&opts.format, "format", "", "")
	fs.StringVar(&opts.format, "f", "", "")
	fs.IntVar(&opts.compression, "compression", -1, "")
	fs.BoolVar(&opts.transparent, "transparent", false, "")
	fs.BoolVar(&opts.stream, "stream", false, "")
	fs.IntVar(&opts.partials, "partials", 0, "")
	fs.BoolVar(&opts.progress, "progress", false, "")
	fs.StringVar(&opts.output, "output", ".", "")
	fs.StringVar(&opts.output, "o", ".", "")
	fs.BoolVar(&opts.jsonOut, "json", false, "")
	fs.BoolVar(&opts.showVersion, "version", false, "")
	fs.BoolVar(&opts.showVersion, "v", false, "")
	fs.BoolVar(&opts.showHelp, "help", false, "")
	fs.BoolVar(&opts.showHelp, "h", false, "")

	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "partials" {
			opts.partialsSet = true
		}
	})
	if opts.n < 1 || opts.n > 10 {
		return opts, nil, fmt.Errorf("--n must be between 1 and 10 (got %d)", opts.n)
	}
	if opts.partials < 0 || opts.partials > 3 {
		return opts, nil, fmt.Errorf("--partials must be between 0 and 3 (got %d)", opts.partials)
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

func writeB64(path, data string) error {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}
	return os.WriteFile(path, raw, 0o644)
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
  pix <prompt>                         Generate an image from a prompt
  pix --prompt <prompt>                Same, as a flag
  echo "prompt" | pix                  Read prompt from stdin
  pix -i photo.png "make it cyberpunk" Edit an existing image (image-to-image)
  pix -i a.png -i b.png "combine them" Reference multiple input images

%sOptions:%s
  -m, --model <id>        Model (default: gpt-image-1.5)
                          gpt-image-1.5, gpt-image-1, gpt-image-1-mini
  -p, --prompt <text>     Prompt text (overrides positional / stdin)
  -b, --base <text>       Base prompt prepended to the main prompt
  -i, --image <path>      Input image for editing (repeatable, up to 16)
  -n <1-10>               Number of variants generated in parallel
  -s, --size <preset>     square | landscape | portrait | auto (default: auto)
  -q, --quality <level>   auto | low | medium | high
  -f, --format <ext>      png | jpeg | webp (default: png)
      --compression <n>   0-100 for jpeg / webp
      --transparent       Generate with a transparent background (png/webp only)
      --stream            Stream via SSE (defaults to --partials 2)
      --partials <0-3>    Number of partial frames to save while rendering
      --progress          Shortcut for --stream --partials 3
  -o, --output <dir>      Output directory (default: .)
      --json              Emit a JSON summary to stdout
  -v, --version           Show version
  -h, --help              Show this help

%sExamples:%s
  pix "a corgi astronaut on mars, cinematic"
  pix -n 4 --progress "isometric tiny village"
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
