package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	endpointGenerate = "https://api.openai.com/v1/images/generations"
	endpointEdit     = "https://api.openai.com/v1/images/edits"
)

type Client struct {
	apiKey string
	http   *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

type Request struct {
	Model             string
	Prompt            string
	N                 int
	Size              string
	Quality           string
	OutputFormat      string
	OutputCompression *int
	Background        string
	Moderation        string
	Stream            bool
	PartialImages     *int
	Images            []string // local file paths → routes to /v1/images/edits
}

type ImageData struct {
	B64JSON string `json:"b64_json,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type Response struct {
	Created      int64       `json:"created"`
	Background   string      `json:"background,omitempty"`
	OutputFormat string      `json:"output_format,omitempty"`
	Quality      string      `json:"quality,omitempty"`
	Size         string      `json:"size,omitempty"`
	Data         []ImageData `json:"data"`
	Usage        *Usage      `json:"usage,omitempty"`
}

type apiError struct {
	Err struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func decodeAPIError(body []byte, status int) error {
	var e apiError
	if err := json.Unmarshal(body, &e); err == nil && e.Err.Message != "" {
		return fmt.Errorf("openai %d: %s", status, e.Err.Message)
	}
	snippet := string(body)
	if len(snippet) > 500 {
		snippet = snippet[:500] + "…"
	}
	return fmt.Errorf("openai %d: %s", status, snippet)
}

// Generate routes to the right endpoint based on whether Images is set.
func (c *Client) Generate(ctx context.Context, r Request) (*Response, error) {
	r.Stream = false
	r.PartialImages = nil

	body, ctype, err := c.buildBody(r)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, r, body, ctype, "application/json")
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeAPIError(raw, resp.StatusCode)
	}

	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// endpointFor picks the endpoint based on whether input images are attached.
func endpointFor(r Request) string {
	if len(r.Images) > 0 {
		return endpointEdit
	}
	return endpointGenerate
}

func (c *Client) newRequest(ctx context.Context, r Request, body io.Reader, contentType, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointFor(r), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return req, nil
}

// buildBody constructs either a JSON body (generations) or multipart body (edits).
func (c *Client) buildBody(r Request) (io.Reader, string, error) {
	if len(r.Images) > 0 {
		return buildMultipart(r)
	}
	return buildJSON(r)
}

func buildJSON(r Request) (io.Reader, string, error) {
	payload := map[string]any{
		"model":  r.Model,
		"prompt": r.Prompt,
	}
	if r.N > 0 {
		payload["n"] = r.N
	}
	if r.Size != "" {
		payload["size"] = r.Size
	}
	if r.Quality != "" {
		payload["quality"] = r.Quality
	}
	if r.OutputFormat != "" {
		payload["output_format"] = r.OutputFormat
	}
	if r.OutputCompression != nil {
		payload["output_compression"] = *r.OutputCompression
	}
	if r.Background != "" {
		payload["background"] = r.Background
	}
	if r.Moderation != "" {
		payload["moderation"] = r.Moderation
	}
	if r.Stream {
		payload["stream"] = true
	}
	if r.PartialImages != nil {
		payload["partial_images"] = *r.PartialImages
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(buf), "application/json", nil
}

func buildMultipart(r Request) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	writeField := func(key, val string) error {
		if val == "" {
			return nil
		}
		return mw.WriteField(key, val)
	}

	if err := writeField("model", r.Model); err != nil {
		return nil, "", err
	}
	if err := writeField("prompt", r.Prompt); err != nil {
		return nil, "", err
	}
	if r.N > 0 {
		if err := writeField("n", strconv.Itoa(r.N)); err != nil {
			return nil, "", err
		}
	}
	if err := writeField("size", r.Size); err != nil {
		return nil, "", err
	}
	if err := writeField("quality", r.Quality); err != nil {
		return nil, "", err
	}
	if err := writeField("output_format", r.OutputFormat); err != nil {
		return nil, "", err
	}
	if r.OutputCompression != nil {
		if err := writeField("output_compression", strconv.Itoa(*r.OutputCompression)); err != nil {
			return nil, "", err
		}
	}
	if err := writeField("background", r.Background); err != nil {
		return nil, "", err
	}
	if err := writeField("moderation", r.Moderation); err != nil {
		return nil, "", err
	}
	if r.Stream {
		if err := writeField("stream", "true"); err != nil {
			return nil, "", err
		}
	}
	if r.PartialImages != nil {
		if err := writeField("partial_images", strconv.Itoa(*r.PartialImages)); err != nil {
			return nil, "", err
		}
	}

	for _, path := range r.Images {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", fmt.Errorf("open image %s: %w", path, err)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="image[]"; filename=%q`, filepath.Base(path)))
		header.Set("Content-Type", imageMimeType(path))
		fw, err := mw.CreatePart(header)
		if err != nil {
			f.Close()
			return nil, "", err
		}
		if _, err := io.Copy(fw, f); err != nil {
			f.Close()
			return nil, "", fmt.Errorf("read image %s: %w", path, err)
		}
		f.Close()
	}

	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}

// imageMimeType returns the Content-Type OpenAI expects for /v1/images/edits.
// Only png, jpeg, and webp are accepted.
func imageMimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
