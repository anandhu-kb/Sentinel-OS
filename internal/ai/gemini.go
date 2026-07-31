package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type modelListResponse struct {
	Models []struct {
		Name                     string   `json:"name"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} `json:"models"`
}

func getBestModel(apiKey string) (string, error) {
	listURL := "https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey
	resp, err := http.Get(listURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to list models: %s", string(body))
	}

	var list modelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", err
	}

	var validFlashModels []string
	var fallback string

	for _, m := range list.Models {
		isSupported := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				isSupported = true
				break
			}
		}
		if !isSupported {
			continue
		}

		if strings.HasPrefix(m.Name, "models/gemini") {
			if fallback == "" {
				fallback = m.Name
			}
			if strings.Contains(m.Name, "flash") && !strings.Contains(m.Name, "exp") && !strings.Contains(m.Name, "vision") {
				validFlashModels = append(validFlashModels, m.Name)
			}
		}
	}

	if len(validFlashModels) > 0 {
		// Sort in descending order to pick the highest version (e.g. gemini-3.0-flash over gemini-2.5-flash)
		sort.Slice(validFlashModels, func(i, j int) bool {
			return validFlashModels[i] > validFlashModels[j]
		})
		return validFlashModels[0], nil
	}

	if fallback != "" {
		return fallback, nil
	}

	return "", fmt.Errorf("no suitable gemini model found in region")
}

func GenerateCommitMessage(diffText, apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("API key is required")
	}

	modelName, err := getBestModel(apiKey)
	if err != nil {
		return "", fmt.Errorf("model resolution failed: %v", err)
	}

	url := "https://generativelanguage.googleapis.com/v1beta/" + modelName + ":generateContent?key=" + apiKey

	prompt := "You are an expert developer. Generate a very concise, 1-line git commit message (max 10 words) summarizing the most important change based on the following code diff. Example: 'Fix syntax error and commit new logic'. Do not include explanations, bullet points, or any extra text. Diff:\n\n" + diffText

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{
				Parts: []geminiPart{
					{Text: prompt},
				},
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Gemini API error: %s", string(bodyBytes))
	}

	var result geminiResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	if len(result.Candidates) > 0 && len(result.Candidates[0].Content.Parts) > 0 {
		return strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text), nil
	}
	return "", fmt.Errorf("no response from Gemini")
}

// GenerateAIReview generates a detailed review (summary, syntax check, logic explanation) in HTML format.
func GenerateAIReview(diffText, apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("API key is required")
	}

	modelName, err := getBestModel(apiKey)
	if err != nil {
		return "", fmt.Errorf("model resolution failed: %v", err)
	}

	url := "https://generativelanguage.googleapis.com/v1beta/" + modelName + ":generateContent?key=" + apiKey

	prompt := "You are an expert developer. Review the code diff and provide a response formatted as raw HTML (without markdown blocks). You MUST wrap each section in <p> tags for spacing. Include exactly three sections:\n<p><b>Summary:</b> [brief summary]</p>\n<p><b>Syntax Check:</b> [If no errors, output exactly 'Looks good! No syntax errors are present.'. If errors exist, list ONLY the errors.]</p>\n<p><b>Logic Explanation:</b> [short explanation]</p>\n\nDiff:\n" + diffText

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{
				Parts: []geminiPart{
					{Text: prompt},
				},
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Gemini API error: %s", string(bodyBytes))
	}

	var result geminiResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	if len(result.Candidates) > 0 && len(result.Candidates[0].Content.Parts) > 0 {
		html := strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text)
		html = strings.TrimPrefix(html, "```html")
		html = strings.TrimSuffix(html, "```")
		return html, nil
	}
	return "", fmt.Errorf("no response from Gemini")
}
