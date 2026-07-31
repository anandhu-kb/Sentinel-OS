package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

// GenerateCommitMessageOllama uses a local Ollama instance to generate a commit message.
func GenerateCommitMessageOllama(diffText, modelName string) (string, error) {
	if modelName == "" {
		modelName = "llama3" // Default fallback
	}

	url := "http://127.0.0.1:11434/api/generate"
	prompt := "You are an expert developer. Generate a very concise, 1-line git commit message (max 10 words) summarizing the most important change based on the following code diff. Example: 'Fix syntax error and commit new logic'. Do not include explanations, bullet points, or any extra text. Diff:\n\n" + diffText

	reqBody := ollamaRequest{
		Model:  modelName,
		Prompt: prompt,
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("Ollama connection failed (is it running?): %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Ollama API error: %s", string(bodyBytes))
	}

	var result ollamaResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	if result.Error != "" {
		return "", fmt.Errorf("Ollama API error: %s", result.Error)
	}

	if result.Response != "" {
		return strings.TrimSpace(result.Response), nil
	}

	return "", fmt.Errorf("no response from Ollama")
}

// GenerateAIReviewOllama generates a detailed review in HTML format using Ollama.
func GenerateAIReviewOllama(diffText, modelName string) (string, error) {
	if modelName == "" {
		modelName = "llama3"
	}

	url := "http://127.0.0.1:11434/api/generate"
	prompt := "You are an expert developer. Review the code diff and provide a response formatted as raw HTML (without markdown blocks). You MUST wrap each section in <p> tags for spacing. Include exactly three sections:\n<p><b>Summary:</b> [brief summary]</p>\n<p><b>Syntax Check:</b> [If no errors, output exactly 'Looks good! No syntax errors are present.'. If errors exist, list ONLY the errors.]</p>\n<p><b>Logic Explanation:</b> [short explanation]</p>\n\nDiff:\n" + diffText

	reqBody := ollamaRequest{
		Model:  modelName,
		Prompt: prompt,
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("Ollama connection failed (is it running?): %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Ollama API error: %s", string(bodyBytes))
	}

	var result ollamaResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	if result.Error != "" {
		return "", fmt.Errorf("Ollama API error: %s", result.Error)
	}

	if result.Response != "" {
		html := strings.TrimSpace(result.Response)
		html = strings.TrimPrefix(html, "```html")
		html = strings.TrimSuffix(html, "```")
		return html, nil
	}

	return "", fmt.Errorf("no response from Ollama")
}
