package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/azkazamdigital/wa-gateway/config"
)

// Cosmic MCP client — pengganti NVIDIA untuk auto-reply (chat_text) dan analisa gambar (analyze_image).
// Endpoint: Streamable HTTP MCP (JSON-RPC 2.0) di gen.azkazamdigital.com/mcp.

const (
	cosmicMCPDefaultURL = "https://gen.azkazamdigital.com/mcp"
)

var cosmicMCPKey = "" // diisi via SetCosmicMCPKey dari config

// SetCosmicMCPKey sets the Cosmic MCP bearer key (called from package init in ai).
func SetCosmicMCPKey(k string) {
	cosmicMCPKey = strings.TrimSpace(k)
}

func init() {
	cosmicMCPKey = config.CosmicMCPKey
}

// CosmicMCPEnabled reports whether Cosmic MCP is configured.
func CosmicMCPEnabled() bool {
	return cosmicMCPKey != ""
}

type cosmicMCPClient struct {
	endpoint string
	key      string
	client   *http.Client
}

func newCosmicMCPClient() *cosmicMCPClient {
	return &cosmicMCPClient{
		endpoint: cosmicMCPDefaultURL,
		key:      cosmicMCPKey,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// callTool performs a single JSON-RPC tools/call against the Cosmic MCP endpoint.
func (c *cosmicMCPClient) callTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	if c.key == "" {
		return "", fmt.Errorf("cosmic mcp key belum diisi")
	}
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      name,
			"arguments": args,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cosmic mcp status %d: %s", resp.StatusCode, truncateSingleLine(string(respBody), 200))
	}

	raw := strings.TrimSpace(string(respBody))
	// Handle SSE response (data: lines)
	if strings.HasPrefix(raw, "event:") || strings.Contains(raw, "\ndata:") || strings.HasPrefix(raw, "data:") {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				raw = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				break
			}
		}
	}

	var rpc struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &rpc); err != nil {
		return "", fmt.Errorf("cosmic mcp respons tidak valid: %w", err)
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("cosmic mcp error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil {
		return "", fmt.Errorf("cosmic mcp respons kosong")
	}
	if rpc.Result.IsError {
		msg := ""
		if len(rpc.Result.Content) > 0 {
			msg = rpc.Result.Content[0].Text
		}
		return "", fmt.Errorf("cosmic mcp tool error: %s", msg)
	}
	var out strings.Builder
	for _, part := range rpc.Result.Content {
		if part.Type == "text" {
			out.WriteString(part.Text)
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("cosmic mcp mengembalikan hasil kosong")
	}
	return text, nil
}

// cosmicChatCompletion replaces doNvidiaChatCompletion: flattens OpenAI-style
// messages (system/history/user) into a single chat_text prompt.
func (s *Service) cosmicChatCompletion(ctx context.Context, messages []map[string]interface{}) (string, error) {
	c := newCosmicMCPClient()
	var sb strings.Builder
	for _, m := range messages {
		role, _ := m["role"].(string)
		content := ""
		switch v := m["content"].(type) {
		case string:
			content = v
		default:
			if v != nil {
				b, err := json.Marshal(v)
				if err == nil {
					content = string(b)
				}
			}
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		switch role {
		case "system":
			sb.WriteString("[INSTRUKSI]\n" + content + "\n\n")
		case "assistant":
			sb.WriteString("[Balasan assistant sebelumnya]\n" + content + "\n\n")
		default:
			sb.WriteString("[Pesan user]\n" + content + "\n\n")
		}
	}
	sb.WriteString("[Tugas]\nBalas pesan user terakhir di atas sesuai instruksi. Gunakan bahasa Indonesia yang natural dan sopan.")
	return c.callTool(ctx, "chat_text", map[string]interface{}{
		"prompt": sb.String(),
	})
}

// cosmicVisionAnalysis replaces runVisionAnalysis using analyze_image with
// {prompt, image:{mimeType, base64}}.
func (s *Service) cosmicVisionAnalysis(ctx context.Context, imageData []byte, mimeType, caption string) (string, error) {
	if len(imageData) == 0 {
		return "", fmt.Errorf("data gambar kosong")
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(imageData)
	}
	prompt := "Analisa gambar ini dengan teliti dalam bahasa Indonesia. Baca detail dulu sebelum menyimpulkan. Jika ada teks, salin teks penting secara akurat. Jika ada angka, harga, ukuran, warna, nama produk, nama toko, bukti transfer, resi, alamat, atau status pembayaran, sebutkan jelas. Jika ini screenshot chat/promosi/produk/dokumen, ringkas poin pentingnya secara rapi lalu simpulkan konteks utama gambar."
	if strings.TrimSpace(caption) != "" {
		prompt += "\nCaption user: " + strings.TrimSpace(caption)
	}
	c := newCosmicMCPClient()
	return c.callTool(ctx, "analyze_image", map[string]interface{}{
		"prompt": prompt,
		"image": map[string]string{
			"mimeType": mimeType,
			"base64":   base64.StdEncoding.EncodeToString(imageData),
		},
	})
}

// CosmicChatText runs a single prompt through Cosmic MCP chat_text
// (dipakai assistant broadcast di cmd layer).
func (s *Service) CosmicChatText(ctx context.Context, prompt string) (string, error) {
	if !CosmicMCPEnabled() {
		return "", fmt.Errorf("AI belum dikonfigurasi (COSMIC_MCP_KEY kosong)")
	}
	c := newCosmicMCPClient()
	return c.callTool(ctx, "chat_text", map[string]interface{}{
		"prompt": prompt,
	})
}
