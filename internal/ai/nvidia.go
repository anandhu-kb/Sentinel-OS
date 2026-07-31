package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sentinel-os/internal/logger"
)

type nvidiaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type nvidiaRequest struct {
	Model       string          `json:"model"`
	Messages    []nvidiaMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
}

type nvidiaResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// GenerateCommitMessageNvidia uses Nvidia's OpenAI-compatible API to generate a commit message.
func GenerateCommitMessageNvidia(diffText, config string) (string, error) {
	parts := strings.SplitN(config, "|", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid Nvidia config format. Expected 'apiKey|modelName'")
	}
	apiKey := parts[0]
	modelName := parts[1]

	if apiKey == "" {
		return "", fmt.Errorf("Nvidia API key is required")
	}

	if len(diffText) > 4000 {
		diffText = diffText[:4000] + "\n...[truncated for length]"
	}

	url := "https://integrate.api.nvidia.com/v1/chat/completions"

	logger.Request("Nvidia", "POST", url)
	logger.Debug("Nvidia commit msg: model=%s diffLen=%d", modelName, len(diffText))

	prompt := "You are an expert developer. Generate a very concise, 1-line git commit message (max 10 words) summarizing the most important change based on the following code diff. Example: 'Fix syntax error and commit new logic'. Do not include explanations, bullet points, or any extra text. Diff:\n\n" + diffText

	reqBody := nvidiaRequest{
		Model: modelName,
		Messages: []nvidiaMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   50,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Nvidia commit API call failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	logger.Response("Nvidia", resp.StatusCode)
	if resp.StatusCode != 200 {
		logger.Error("Nvidia commit API body: %s", string(bodyBytes))
		return "", fmt.Errorf("Nvidia API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var result nvidiaResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	if len(result.Choices) > 0 {
		return strings.TrimSpace(result.Choices[0].Message.Content), nil
	}
	return "", fmt.Errorf("no response from Nvidia API")
}

// GenerateAIReviewNvidia generates a detailed review in HTML format using Nvidia's API.
func GenerateAIReviewNvidia(diffText, config string) (string, error) {
	parts := strings.SplitN(config, "|", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid Nvidia config format. Expected 'apiKey|modelName'")
	}
	apiKey := parts[0]
	modelName := parts[1]

	if apiKey == "" {
		return "", fmt.Errorf("Nvidia API key is required")
	}

	if len(diffText) > 4000 {
		diffText = diffText[:4000] + "\n...[truncated for length]"
	}

	url := "https://integrate.api.nvidia.com/v1/chat/completions"

	logger.Request("Nvidia", "POST", url)
	logger.Debug("Nvidia AI review: model=%s diffLen=%d", modelName, len(diffText))

	prompt := "You are an expert developer. Review the code diff and provide a response formatted as raw HTML (without markdown blocks). You MUST wrap each section in <p> tags for spacing. Include exactly three sections:\n<p><b>Summary:</b> [brief summary]</p>\n<p><b>Syntax Check:</b> [If no errors, output exactly 'Looks good! No syntax errors are present.'. If errors exist, list ONLY the errors.]</p>\n<p><b>Logic Explanation:</b> [short explanation]</p>\n\nDiff:\n" + diffText

	reqBody := nvidiaRequest{
		Model: modelName,
		Messages: []nvidiaMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   1000,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Nvidia review API call failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	logger.Response("Nvidia", resp.StatusCode)
	if resp.StatusCode != 200 {
		logger.Error("Nvidia review API body: %s", string(bodyBytes))
		return "", fmt.Errorf("Nvidia API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var result nvidiaResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	if len(result.Choices) > 0 {
		html := strings.TrimSpace(result.Choices[0].Message.Content)
		html = strings.TrimPrefix(html, "```html")
		html = strings.TrimSuffix(html, "```")
		return html, nil
	}
	return "", fmt.Errorf("no response from Nvidia API")
}
