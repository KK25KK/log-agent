package volcark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	DefaultBaseURL     = "https://ark.cn-beijing.volces.com/api/v3"
	maxRequestBytes    = 32 * 1024
	maxResponseBytes   = 128 * 1024
	maxOutputTokens    = 1200
	evidencePromptText = `你是内部日志调查报告摘要器。只允许改写输入 JSON 中已经存在的事实，不得生成新事实、查询、权限、置信度、原因结论或处置动作。输出必须是单个 JSON 对象，且只能有 phenomenon、phenomenon_evidence_ids、cause_hypothesis_id、evidence_notes、recommendation_codes 五个字段。phenomenon 必须引用已有 evidence ID；cause_hypothesis_id 只能从 SUPPORTED_CANDIDATE 中选择或留空；evidence_notes 每项只能有 statement、evidence_ids；recommendation_codes 只能从输入中选择。不得输出 Markdown、URL、命令、原始日志或 JSON 之外的文本。`
)

type Config struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

type Summarizer struct {
	apiKey     string
	model      string
	endpoint   string
	client     *http.Client
	promptHash string
}

type responsesRequest struct {
	Model           string `json:"model"`
	Input           string `json:"input"`
	Store           bool   `json:"store"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type responsesEnvelope struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

func New(config Config) (*Summarizer, error) {
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	if config.Timeout <= 0 {
		config.Timeout = 12 * time.Second
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, errors.New("Volcengine Ark base URL must be an HTTPS origin")
	}
	client := &http.Client{
		Timeout: config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Volcengine Ark redirects are disabled")
		},
	}
	return newSummarizer(config.APIKey, config.Model, strings.TrimRight(config.BaseURL, "/")+"/responses", client, false)
}

func newSummarizer(apiKey, model, endpoint string, client *http.Client, allowHTTP bool) (*Summarizer, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil ||
		(parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http")) {
		return nil, errors.New("Volcengine Ark endpoint is invalid")
	}
	if apiKey == "" || !safeIdentifier(model, 1, 160) || client == nil {
		return nil, errors.New("Volcengine Ark API key, model, and HTTP client are required")
	}
	digest := sha256.Sum256([]byte(evidencePromptText))
	return &Summarizer{
		apiKey: apiKey, model: model, endpoint: endpoint, client: client,
		promptHash: hex.EncodeToString(digest[:]),
	}, nil
}

func (summarizer *Summarizer) Summarize(ctx context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return domain.SummaryProviderResult{}, errors.New("encode governed summary input")
	}
	requestBody, err := json.Marshal(responsesRequest{
		Model: summarizer.model,
		Input: evidencePromptText + "\n\n受治理输入 JSON：\n" + string(inputJSON),
		Store: false, MaxOutputTokens: maxOutputTokens,
	})
	if err != nil || len(requestBody) > maxRequestBytes {
		return domain.SummaryProviderResult{}, errors.New("governed summary request exceeds limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, summarizer.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return domain.SummaryProviderResult{}, errors.New("create Volcengine Ark request")
	}
	req.Header.Set("Authorization", "Bearer "+summarizer.apiKey)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := summarizer.client.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return domain.SummaryProviderResult{}, errors.New("Volcengine Ark request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return domain.SummaryProviderResult{}, fmt.Errorf("Volcengine Ark request rejected with status %d", resp.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return domain.SummaryProviderResult{}, errors.New("Volcengine Ark response exceeds limit")
	}
	var envelope responsesEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || !strings.EqualFold(envelope.Status, "completed") {
		return domain.SummaryProviderResult{}, errors.New("Volcengine Ark response is incomplete")
	}
	text := responseText(envelope)
	if text == "" {
		return domain.SummaryProviderResult{}, errors.New("Volcengine Ark response has no output text")
	}
	draft, err := decodeDraft([]byte(text))
	if err != nil {
		return domain.SummaryProviderResult{}, errors.New("Volcengine Ark returned an invalid summary object")
	}
	model := envelope.Model
	if !safeIdentifier(model, 1, 160) {
		model = summarizer.model
	}
	return domain.SummaryProviderResult{
		Draft: draft, Mode: domain.SummaryModeModel,
		Provider: "volcengine_ark", Model: model, RequestID: boundedIdentifier(envelope.ID, 256),
		PromptVersion: domain.EvidenceSummaryPromptVersion, PromptFingerprint: summarizer.promptHash,
		InputTokens: envelope.Usage.InputTokens, OutputTokens: envelope.Usage.OutputTokens,
		TotalTokens: envelope.Usage.TotalTokens, LatencyMillis: latency,
	}, nil
}

func responseText(envelope responsesEnvelope) string {
	for _, output := range envelope.Output {
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "output_text" && content.Text != "" {
				return content.Text
			}
		}
	}
	return ""
}

func decodeDraft(payload []byte) (domain.SummaryDraft, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var draft domain.SummaryDraft
	if err := decoder.Decode(&draft); err != nil {
		return domain.SummaryDraft{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return domain.SummaryDraft{}, errors.New("summary response contains trailing data")
	}
	return draft, nil
}

func safeIdentifier(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_-.:/", character) {
			continue
		}
		return false
	}
	return true
}

func boundedIdentifier(value string, maximum int) string {
	if safeIdentifier(value, 0, maximum) {
		return value
	}
	return ""
}

var _ ports.ReportSummarizer = (*Summarizer)(nil)
