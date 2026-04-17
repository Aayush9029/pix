package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type StreamEventKind int

const (
	EventPartial StreamEventKind = iota
	EventCompleted
)

type StreamEvent struct {
	Kind              StreamEventKind
	B64JSON           string
	PartialImageIndex int
	Size              string
	Quality           string
	Background        string
	OutputFormat      string
	Usage             *Usage
}

type streamEnvelope struct {
	Type              string `json:"type"`
	B64JSON           string `json:"b64_json"`
	PartialImageIndex int    `json:"partial_image_index"`
	Size              string `json:"size"`
	Quality           string `json:"quality"`
	Background        string `json:"background"`
	OutputFormat      string `json:"output_format"`
	Usage             *Usage `json:"usage"`
}

// GenerateStream opens an SSE stream and invokes onEvent for each parsed event.
// Handles both /v1/images/generations and /v1/images/edits (event type prefixes
// `image_generation.*` and `image_edit.*`).
func (c *Client) GenerateStream(ctx context.Context, r Request, onEvent func(StreamEvent) error) error {
	r.Stream = true

	body, ctype, err := c.buildBody(r)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, r, body, ctype, "text/event-stream")
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return decodeAPIError(raw, resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 32*1024*1024)

	var dataBuf strings.Builder
	flush := func() error {
		if dataBuf.Len() == 0 {
			return nil
		}
		payload := dataBuf.String()
		dataBuf.Reset()
		if payload == "[DONE]" {
			return nil
		}
		var env streamEnvelope
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		ev := StreamEvent{
			B64JSON:           env.B64JSON,
			PartialImageIndex: env.PartialImageIndex,
			Size:              env.Size,
			Quality:           env.Quality,
			Background:        env.Background,
			OutputFormat:      env.OutputFormat,
			Usage:             env.Usage,
		}
		switch env.Type {
		case "image_generation.partial_image", "image_edit.partial_image":
			ev.Kind = EventPartial
		case "image_generation.completed", "image_edit.completed":
			ev.Kind = EventCompleted
		default:
			return nil
		}
		return onEvent(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimPrefix(chunk, " ")
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(chunk)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return flush()
}
