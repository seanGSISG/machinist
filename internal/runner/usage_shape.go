package runner

import (
	"encoding/json"
	"math"

	"github.com/owainlewis/machinist/internal/protocol"
)

// Detect parses usage from a supported structured-output event. Selection is
// based only on the event's JSON shape, so wrapper command lines do not hide
// usage reporting.
func Detect(line []byte) (protocol.Usage, bool) {
	var event struct {
		Type  string          `json:"type"`
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(line, &event) != nil || len(event.Usage) == 0 {
		return protocol.Usage{}, false
	}
	switch event.Type {
	case "turn.completed":
		return detectCodexUsage(event.Model, event.Usage)
	case "result":
		return detectClaudeResultUsage(event.Model, event.Usage)
	case "message_delta":
		return detectClaudeDeltaUsage(event.Model, event.Usage)
	default:
		return protocol.Usage{}, false
	}
}

func detectCodexUsage(model string, raw json.RawMessage) (protocol.Usage, bool) {
	var fields struct {
		Input     *int64 `json:"input_tokens"`
		Output    *int64 `json:"output_tokens"`
		Cached    *int64 `json:"cached_input_tokens"`
		Reasoning *int64 `json:"reasoning_tokens"`
	}
	if json.Unmarshal(raw, &fields) != nil || !validRequiredUsage(fields.Input, fields.Output) || !validOptionalUsage(fields.Cached, fields.Reasoning) {
		return protocol.Usage{}, false
	}
	return protocol.Usage{
		Model:             model,
		InputTokens:       *fields.Input,
		OutputTokens:      *fields.Output,
		CachedInputTokens: valueOrZero(fields.Cached),
		ReasoningTokens:   valueOrZero(fields.Reasoning),
	}, true
}

func detectClaudeResultUsage(model string, raw json.RawMessage) (protocol.Usage, bool) {
	var fields struct {
		Input         *int64 `json:"input_tokens"`
		Output        *int64 `json:"output_tokens"`
		CacheCreation *int64 `json:"cache_creation_input_tokens"`
		CacheRead     *int64 `json:"cache_read_input_tokens"`
	}
	if json.Unmarshal(raw, &fields) != nil || !validRequiredUsage(fields.Input, fields.Output) ||
		fields.CacheCreation == nil || fields.CacheRead == nil || *fields.CacheCreation < 0 || *fields.CacheRead < 0 {
		return protocol.Usage{}, false
	}
	input, ok := addUsage(*fields.Input, *fields.CacheCreation, *fields.CacheRead)
	if !ok {
		return protocol.Usage{}, false
	}
	return protocol.Usage{
		Model:             model,
		InputTokens:       input,
		OutputTokens:      *fields.Output,
		CachedInputTokens: *fields.CacheRead,
	}, true
}

func detectClaudeDeltaUsage(model string, raw json.RawMessage) (protocol.Usage, bool) {
	var fields struct {
		Input         *int64 `json:"input_tokens"`
		Output        *int64 `json:"output_tokens"`
		CacheCreation *int64 `json:"cache_creation_input_tokens"`
		CacheRead     *int64 `json:"cache_read_input_tokens"`
	}
	if json.Unmarshal(raw, &fields) != nil || fields.Output == nil || *fields.Output < 0 || !validOptionalUsage(fields.Input, fields.CacheCreation, fields.CacheRead) {
		return protocol.Usage{}, false
	}
	input, ok := addUsage(valueOrZero(fields.Input), valueOrZero(fields.CacheCreation), valueOrZero(fields.CacheRead))
	if !ok {
		return protocol.Usage{}, false
	}
	return protocol.Usage{
		Model:             model,
		InputTokens:       input,
		OutputTokens:      *fields.Output,
		CachedInputTokens: valueOrZero(fields.CacheRead),
	}, true
}

func validRequiredUsage(values ...*int64) bool {
	for _, value := range values {
		if value == nil || *value < 0 {
			return false
		}
	}
	return true
}

func validOptionalUsage(values ...*int64) bool {
	for _, value := range values {
		if value != nil && *value < 0 {
			return false
		}
	}
	return true
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func addUsage(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > math.MaxInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}
