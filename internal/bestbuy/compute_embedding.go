package bestbuy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os/exec"
	"strings"
)

type ComputeEmbedder interface {
	Embed(ctx context.Context, texts []string) (string, [][]float64, error)
}

func NewComputeEmbedder(command string) ComputeEmbedder {
	command = strings.TrimSpace(command)
	if command != "" {
		return commandComputeEmbedder{command: command}
	}
	return hashComputeEmbedder{dimensions: 128}
}

type hashComputeEmbedder struct {
	dimensions int
}

func (e hashComputeEmbedder) Embed(_ context.Context, texts []string) (string, [][]float64, error) {
	if e.dimensions <= 0 {
		e.dimensions = 128
	}
	vectors := make([][]float64, 0, len(texts))
	for _, text := range texts {
		vector := make([]float64, e.dimensions)
		for _, token := range strings.Fields(strings.ToLower(text)) {
			token = strings.Trim(token, ";:,")
			if token == "" {
				continue
			}
			h := fnv.New32a()
			_, _ = h.Write([]byte(token))
			idx := int(h.Sum32() % uint32(e.dimensions))
			vector[idx]++
		}
		normalizeVector(vector)
		vectors = append(vectors, vector)
	}
	return "local-token-hash-v1", vectors, nil
}

type commandComputeEmbedder struct {
	command string
}

func (e commandComputeEmbedder) Embed(ctx context.Context, texts []string) (string, [][]float64, error) {
	payload, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		return "", nil, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", e.command)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("embedding command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var response struct {
		Model   string      `json:"model"`
		Vectors [][]float64 `json:"vectors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return "", nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(response.Vectors) != len(texts) {
		return "", nil, fmt.Errorf("embedding response vector count %d != text count %d", len(response.Vectors), len(texts))
	}
	for i := range response.Vectors {
		normalizeVector(response.Vectors[i])
	}
	if response.Model == "" {
		response.Model = "external-local-embedding"
	}
	return response.Model, response.Vectors, nil
}

func normalizeVector(vector []float64) {
	var sumSquares float64
	for _, value := range vector {
		sumSquares += value * value
	}
	if sumSquares == 0 {
		return
	}
	norm := math.Sqrt(sumSquares)
	for i := range vector {
		vector[i] = vector[i] / norm
	}
}
