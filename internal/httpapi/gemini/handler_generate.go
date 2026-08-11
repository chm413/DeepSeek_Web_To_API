package gemini

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	openaishared "DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/sse"
	"DeepSeek_Web_To_API/internal/toolcall"
	"DeepSeek_Web_To_API/internal/translatorcliproxy"
	"DeepSeek_Web_To_API/internal/util"
)

func (h *Handler) handleGenerateContent(w http.ResponseWriter, r *http.Request, stream bool) {
	if h.OpenAI == nil {
		writeGeminiError(w, http.StatusInternalServerError, "OpenAI proxy backend unavailable.")
		return
	}
	if h.proxyViaOpenAI(w, r, stream) {
		return
	}
	writeGeminiError(w, http.StatusBadGateway, "Failed to proxy Gemini request.")
}

var geminiProxyMaxBodyBytes int64 = openaishared.GeneralMaxSize

func (h *Handler) proxyViaOpenAI(w http.ResponseWriter, r *http.Request, stream bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, geminiProxyMaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if errors.Is(err, requestbody.ErrInvalidUTF8Body) {
			writeGeminiError(w, http.StatusBadRequest, "invalid json")
		} else if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeGeminiError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeGeminiError(w, http.StatusBadRequest, "invalid body")
		}
		return true
	}
	routeModel := strings.TrimSpace(chi.URLParam(r, "model"))
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		writeGeminiError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	// Gemini clients commonly replay candidates with thought parts intact.
	// Those parts are provider-private reasoning state and the retained DeepSeek
	// branch already owns them. The generic translator otherwise concatenates
	// thought text into an OpenAI assistant content string, which changes the
	// canonical prefix and defeats incremental reuse on the next turn.
	droppedThoughtParts := stripGeminiThoughtParts(req)
	translatedSource := raw
	if droppedThoughtParts > 0 {
		if sanitized, marshalErr := json.Marshal(req); marshalErr == nil {
			translatedSource = sanitized
		}
	}
	translatedReq := translatorcliproxy.ToOpenAI(sdktranslator.FormatGemini, routeModel, translatedSource, stream)
	if !strings.Contains(string(translatedReq), `"stream"`) {
		var reqMap map[string]any
		if json.Unmarshal(translatedReq, &reqMap) == nil {
			reqMap["stream"] = stream
			if b, e := json.Marshal(reqMap); e == nil {
				translatedReq = b
			}
		}
	}
	translatedReq = applyGeminiThinkingPolicyToOpenAIRequest(translatedReq, req)
	requestSum := sha256.Sum256(raw)
	config.Logger.Info("[incremental] adapter request context",
		"surface", "gemini.generate_content", "model", routeModel,
		"stream", stream, "wire_request_bytes", len(raw),
		"wire_request_sha256", hex.EncodeToString(requestSum[:8]),
		"translated_request_bytes", len(translatedReq),
		"dropped_thought_parts", droppedThoughtParts)

	proxyReq := r.Clone(openaishared.WithCurrentInputFileSkipped(r.Context()))
	proxyReq.URL.Path = "/v1/chat/completions"
	proxyReq.Body = io.NopCloser(bytes.NewReader(translatedReq))
	proxyReq.ContentLength = int64(len(translatedReq))
	proxyReq.Header.Set(auth.SessionAffinityHeader, geminiSessionAffinityScope(routeModel, req))

	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		streamWriter := translatorcliproxy.NewOpenAIStreamTranslatorWriter(w, sdktranslator.FormatGemini, routeModel, raw, translatedReq)
		h.OpenAI.ChatCompletions(streamWriter, proxyReq)
		return true
	}

	rec := httptest.NewRecorder()
	h.OpenAI.ChatCompletions(rec, proxyReq)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		for k, vv := range res.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		writeGeminiErrorFromOpenAI(w, res.StatusCode, body)
		return true
	}
	converted := translatorcliproxy.FromOpenAINonStream(sdktranslator.FormatGemini, routeModel, raw, translatedReq, body)
	if !json.Valid(converted) {
		writeGeminiError(w, http.StatusBadGateway, "invalid translated Gemini response")
		return true
	}
	// Some HTTP clients, notably Windows PowerShell 5.1, decode a bare
	// application/json response using a legacy code page. Gemini responses are
	// routinely replayed as conversation history, so declare UTF-8 explicitly
	// to preserve the exact assistant prefix required by incremental reuse.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- converted is validated JSON and served as application/json with nosniff.
	_, _ = w.Write(converted)
	return true
}

// stripGeminiThoughtParts removes text that is explicitly marked as Gemini
// thought/reasoning from replayed conversation turns. A mixed tool-call part
// is retained with only its private text removed, so tool continuation data is
// not discarded.
func stripGeminiThoughtParts(req map[string]any) int {
	if req == nil {
		return 0
	}
	contents, ok := req["contents"].([]any)
	if !ok {
		return 0
	}
	dropped := 0
	for _, rawContent := range contents {
		content, ok := rawContent.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]any)
		if !ok || len(parts) == 0 {
			continue
		}
		kept := make([]any, 0, len(parts))
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok || !geminiThoughtPart(part) {
				kept = append(kept, rawPart)
				continue
			}
			dropped++
			copyPart := cloneGeminiMap(part)
			delete(copyPart, "thought")
			delete(copyPart, "isThought")
			delete(copyPart, "is_thought")
			delete(copyPart, "text")
			if len(copyPart) > 0 {
				kept = append(kept, copyPart)
			}
		}
		content["parts"] = kept
	}
	return dropped
}

func geminiThoughtPart(part map[string]any) bool {
	for _, key := range []string{"thought", "isThought", "is_thought"} {
		switch value := part[key].(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(value), "true") {
				return true
			}
		}
	}
	return false
}

func cloneGeminiMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func applyGeminiThinkingPolicyToOpenAIRequest(translated []byte, original map[string]any) []byte {
	req := map[string]any{}
	if err := json.Unmarshal(translated, &req); err != nil {
		return translated
	}
	enabled, ok := resolveGeminiThinkingOverride(original)
	if !ok {
		return translated
	}
	typ := "disabled"
	if enabled {
		typ = "enabled"
	}
	req["thinking"] = map[string]any{"type": typ}
	out, err := json.Marshal(req)
	if err != nil {
		return translated
	}
	return out
}

func resolveGeminiThinkingOverride(req map[string]any) (bool, bool) {
	generationConfig, ok := req["generationConfig"].(map[string]any)
	if !ok {
		generationConfig, ok = req["generation_config"].(map[string]any)
	}
	if !ok {
		return false, false
	}
	thinkingConfig, ok := generationConfig["thinkingConfig"].(map[string]any)
	if !ok {
		thinkingConfig, ok = generationConfig["thinking_config"].(map[string]any)
	}
	if !ok {
		return false, false
	}
	budget, ok := numericAny(thinkingConfig["thinkingBudget"])
	if !ok {
		budget, ok = numericAny(thinkingConfig["thinking_budget"])
	}
	if !ok {
		return false, false
	}
	return budget > 0, true
}

func numericAny(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func writeGeminiErrorFromOpenAI(w http.ResponseWriter, status int, raw []byte) {
	message := strings.TrimSpace(string(raw))
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if errObj, ok := parsed["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
				message = strings.TrimSpace(msg)
			}
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	writeGeminiError(w, status, message)
}

//nolint:unused // retained for native Gemini non-stream handling path.
func (h *Handler) handleNonStreamGenerateContent(w http.ResponseWriter, resp *http.Response, model, finalPrompt string, thinkingEnabled bool, toolNames []string) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeGeminiError(w, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}

	result := sse.CollectStream(resp, thinkingEnabled, true)
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	writeJSON(w, http.StatusOK, buildGeminiGenerateContentResponse(
		model,
		finalPrompt,
		cleanVisibleOutput(result.Thinking, stripReferenceMarkers),
		cleanVisibleOutput(result.Text, stripReferenceMarkers),
		toolNames,
	))
}

//nolint:unused // retained for native Gemini non-stream handling path.
func buildGeminiGenerateContentResponse(model, finalPrompt, finalThinking, finalText string, toolNames []string) map[string]any {
	parts := buildGeminiPartsFromFinal(finalText, finalThinking, toolNames)
	usage := buildGeminiUsage(model, finalPrompt, finalThinking, finalText)
	return map[string]any{
		"candidates": []map[string]any{
			{
				"index": 0,
				"content": map[string]any{
					"role":  "model",
					"parts": parts,
				},
				"finishReason": "STOP",
			},
		},
		"modelVersion":  model,
		"usageMetadata": usage,
	}
}

//nolint:unused // retained for native Gemini non-stream handling path.
func buildGeminiUsage(model, finalPrompt, finalThinking, finalText string) map[string]any {
	promptTokens := util.CountPromptTokens(finalPrompt, model)
	reasoningTokens := util.CountOutputTokens(finalThinking, model)
	completionTokens := util.CountOutputTokens(finalText, model)
	return map[string]any{
		"promptTokenCount":     promptTokens,
		"candidatesTokenCount": reasoningTokens + completionTokens,
		"totalTokenCount":      promptTokens + reasoningTokens + completionTokens,
	}
}

//nolint:unused // retained for native Gemini non-stream handling path.
func buildGeminiPartsFromFinal(finalText, finalThinking string, toolNames []string) []map[string]any {
	detected := toolcall.ParseToolCalls(finalText, toolNames)
	if len(detected) == 0 && finalThinking != "" {
		detected = toolcall.ParseToolCalls(finalThinking, toolNames)
	}
	if len(detected) > 0 {
		parts := make([]map[string]any, 0, len(detected))
		for _, tc := range detected {
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"name": tc.Name,
					"args": tc.Input,
				},
			})
		}
		return parts
	}

	text := finalText
	if text == "" {
		text = finalThinking
	}
	return []map[string]any{{"text": text}}
}
