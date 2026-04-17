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

type options struct {
	model         string
	prompt        string
	base          string
	n             int
	size          string
	quality       string
	format        string
	background    string
	compression   int
	moderation    string
	stream        bool
	partials      int
	progress      bool
	savePartials  bool
	output        string
	name          string
	jsonOut       bool
	showVersion   bool
	showHelp      bool
	promptFromArg bool
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

	outDir, err := config.ResolveOutputDir(opts.output)
	if err != nil {
		return err
	}

	// Decide streaming behavior.
	model := opts.model
	streamingCapable := strings.HasPrefix(model, "gpt-image-")
	streamRequested := opts.stream || opts.progress || opts.partials > 0
	if streamRequested && !streamingCapable {
		ui.Status(fmt.Sprintf("streaming not supported by %s — falling back to non-streaming", model))
		streamRequested = false
	}
	partials := opts.partials
	if opts.progress && partials == 0 {
		partials = 3
	}

	format := opts.format
	if format == "" && streamingCapable {
		format = "png"
	}
	if opts.background == "transparent" && format == "jpeg" {
		return errors.New("background=transparent requires format png or webp")
	}

	req := api.Request{
		Model:      model,
		Prompt:     prompt,
		N:          1,
		Size:       opts.size,
		Quality:    opts.quality,
		Background: nonEmpty(streamingCapable, opts.background),
		Moderation: nonEmpty(streamingCapable, opts.moderation),
	}
	if streamingCapable {
		req.OutputFormat = format
		if opts.compression >= 0 && opts.compression <= 100 && (format == "jpeg" || format == "webp") {
			c := opts.compression
			req.OutputCompression = &c
		}
	} else {
		// dall-e needs b64 so we can save to disk.
		req.ResponseFormat = "b64_json"
	}

	stamp := time.Now().Format("20060102-150405")
	baseName := opts.name
	if baseName == "" {
		baseName = sanitizeName(prompt)
	}

	client := api.NewClient(apiKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ui.Header(fmt.Sprintf("pix · %s · %dx", model, opts.n))
	if ui.IsTTY() {
		ui.Dimf("  prompt: %s", truncate(prompt, 80))
		if opts.size != "" {
			ui.Dimf("  size:   %s", opts.size)
		}
		if opts.quality != "" {
			ui.Dimf("  quality: %s", opts.quality)
		}
		fmt.Println()
	}

	type variantResult struct {
		index   int
		savedAt string
		partial []string
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
				var partialPaths []string
				var usage *api.Usage

				err := client.GenerateStream(ctx, variantReq, func(ev api.StreamEvent) error {
					switch ev.Kind {
					case api.EventPartial:
						printMu.Lock()
						ui.Status(fmt.Sprintf("%spartial %d received", tag, ev.PartialImageIndex+1))
						printMu.Unlock()
						if opts.savePartials && ev.B64JSON != "" {
							ext := ev.OutputFormat
							if ext == "" {
								ext = format
							}
							path := filepath.Join(outDir, fmt.Sprintf("%s-p%d.%s", filename, ev.PartialImageIndex+1, ext))
							if err := writeB64(path, ev.B64JSON); err != nil {
								return err
							}
							partialPaths = append(partialPaths, path)
						}
					case api.EventCompleted:
						ext := ev.OutputFormat
						if ext == "" {
							ext = format
						}
						path := filepath.Join(outDir, filename+"."+ext)
						if err := writeB64(path, ev.B64JSON); err != nil {
							return err
						}
						savedPath = path
						usage = ev.Usage
					}
					return nil
				})
				results[idx] = variantResult{index: idx, savedAt: savedPath, partial: partialPaths, usage: usage, err: err}
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
				ext = fallback(format, "png")
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
			"model":        model,
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
	// stdin
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

// --- flag parsing ---

func parseFlags(args []string) (options, []string, error) {
	var opts options
	fs := flag.NewFlagSet("pix", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // silence default error print; we format errors ourselves

	fs.StringVar(&opts.model, "model", defaultModel, "")
	fs.StringVar(&opts.model, "m", defaultModel, "")
	fs.StringVar(&opts.prompt, "prompt", "", "")
	fs.StringVar(&opts.prompt, "p", "", "")
	fs.StringVar(&opts.base, "base", "", "")
	fs.StringVar(&opts.base, "b", "", "")
	fs.IntVar(&opts.n, "n", 1, "")
	fs.StringVar(&opts.size, "size", "", "")
	fs.StringVar(&opts.size, "s", "", "")
	fs.StringVar(&opts.quality, "quality", "", "")
	fs.StringVar(&opts.quality, "q", "", "")
	fs.StringVar(&opts.format, "format", "", "")
	fs.StringVar(&opts.format, "f", "", "")
	fs.StringVar(&opts.background, "background", "", "")
	fs.IntVar(&opts.compression, "compression", -1, "")
	fs.StringVar(&opts.moderation, "moderation", "", "")
	fs.BoolVar(&opts.stream, "stream", false, "")
	fs.IntVar(&opts.partials, "partials", 0, "")
	fs.BoolVar(&opts.progress, "progress", false, "")
	fs.BoolVar(&opts.savePartials, "save-partials", false, "")
	fs.StringVar(&opts.output, "output", ".", "")
	fs.StringVar(&opts.output, "o", ".", "")
	fs.StringVar(&opts.name, "name", "", "")
	fs.BoolVar(&opts.jsonOut, "json", false, "")
	fs.BoolVar(&opts.showVersion, "version", false, "")
	fs.BoolVar(&opts.showVersion, "v", false, "")
	fs.BoolVar(&opts.showHelp, "help", false, "")
	fs.BoolVar(&opts.showHelp, "h", false, "")

	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
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

func nonEmpty(enabled bool, v string) string {
	if !enabled {
		return ""
	}
	return v
}

func fallback(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func showHelp() {
	fmt.Printf(`%spix%s v%s — OpenAI image generation for the terminal

%sUsage:%s
  pix <prompt>                         Generate an image from a prompt
  pix --prompt <prompt>                Same, as a flag
  echo "prompt" | pix                  Read prompt from stdin
  pix -n 4 --progress <prompt>         4 variants, streamed with partial previews

%sOptions:%s
  -m, --model <id>        Model (default: gpt-image-1.5)
                          gpt-image-1.5, gpt-image-1, gpt-image-1-mini,
                          dall-e-3, dall-e-2
  -p, --prompt <text>     Prompt text (overrides positional / stdin)
  -b, --base <text>       Base prompt prepended to the main prompt
  -n <1-10>               Number of variants to generate in parallel
  -s, --size <wxh>        1024x1024, 1536x1024, 1024x1536, or auto
  -q, --quality <level>   auto | low | medium | high (gpt-image)
                          standard | hd (dall-e-3)
  -f, --format <ext>      png | jpeg | webp (gpt-image only)
      --background <v>    auto | transparent | opaque
      --compression <n>   0-100 for jpeg/webp
      --moderation <v>    auto | low
      --stream            Enable Server-Sent Event streaming
      --partials <0-3>    Number of partial frames to emit (implies stream)
      --progress          Shortcut for --stream --partials 3
      --save-partials     Also save intermediate frames to disk
  -o, --output <dir>      Output directory (default: .)
      --name <prefix>     Filename prefix (default: derived from prompt)
      --json              Emit a JSON summary to stdout
  -v, --version           Show version
  -h, --help              Show this help

%sExamples:%s
  pix "a corgi astronaut on mars, cinematic"
  pix -n 4 --progress "isometric tiny village"
  echo "neon koi fish" | pix -s 1536x1024 -q high
  pix -b "studio ghibli style" -p "rainy train station"

%sRequires OPENAI_API_KEY in the environment.%s
`, ui.Bold, ui.Reset, version,
		ui.Bold, ui.Reset,
		ui.Bold, ui.Reset,
		ui.Bold, ui.Reset,
		ui.Dim, ui.Reset)
}
