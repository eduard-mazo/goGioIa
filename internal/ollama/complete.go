package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Complete genera una respuesta completa (consumiendo el stream): para
// trabajos en segundo plano como los resúmenes de conversación. Pasa por el
// mismo semáforo de concurrencia que las generaciones interactivas.
func (c *Client) Complete(ctx context.Context, model string, msgs []Message, options map[string]any) (string, error) {
	body, err := c.Stream(ctx, model, msgs, options)
	if err != nil {
		return "", err
	}
	defer body.Close()

	var out strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ChatChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		if chunk.Error != "" {
			return "", fmt.Errorf("ollama: %s", chunk.Error)
		}
		out.WriteString(chunk.Message.Content)
		if chunk.Done {
			return out.String(), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return out.String(), nil
}
