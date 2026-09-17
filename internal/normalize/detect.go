package normalize

import (
	"net/http"
	"strings"
)

func Detect(path string, body map[string]any) Format {
	path = strings.ToLower(path)
	if strings.Contains(path, "/v1/responses") {
		return FormatOpenAIResponses
	}
	if strings.Contains(path, "/v1/messages") {
		return FormatAnthropic
	}
	if strings.Contains(path, "/v1/chat/completions") && body != nil {
		if _, ok := body["input"]; ok {
			return FormatOpenAIChat
		}
	}
	if body != nil {
		if _, ok := body["request"]; ok {
			if _, nested := body["contents"]; nested {
				return FormatAntigravity
			}
		}
		if _, ok := body["contents"]; ok {
			return FormatGemini
		}
		if _, ok := body["input"]; ok {
			return FormatOpenAIResponses
		}
		if bodyHasClaudeShape(body) {
			return FormatAnthropic
		}
	}
	return FormatOpenAIChat
}

func DetectWithHeaders(path string, body map[string]any, headers http.Header) Format {
	format := Detect(path, body)
	if format != FormatOpenAIChat {
		return format
	}
	if strings.Contains(strings.ToLower(headers.Get("user-agent")), "gemini-cli") {
		return FormatGeminiCLI
	}
	return format
}

func bodyHasClaudeShape(body map[string]any) bool {
	if _, ok := body["anthropic_version"]; ok {
		return true
	}
	if _, ok := body["system"]; ok {
		_, hasMessages := body["messages"]
		return hasMessages
	}
	return false
}
