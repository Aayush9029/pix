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
// The stream always ends with a completed event; partial events may arrive 0–N times.
func (c *Client) GenerateStream(ctx context.Context, r Request, onEvent func(StreamEvent) error) error {
	r.Stream = true

	req, err := c.newRequest(ctx, r, "text/event-stream")
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return decodeAPIError(body, resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Partial b64 payloads can be multi-MB; give the scanner a large buffer.
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
		case "image_generation.partial_image":
			ev.Kind = EventPartial
		case "image_generation.completed":
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
			continue // SSE comment
		}
		if strings.HasPrefix(line, "data:") {
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimPrefix(chunk, " ")
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(chunk)
		}
		// Ignore other SSE fields (event:, id:, retry:).
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	// Flush any trailing event that didn't end with a blank line.
	return flush()
}
