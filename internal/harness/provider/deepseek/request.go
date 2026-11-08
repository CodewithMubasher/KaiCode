package deepseek

import "kai/internal/harness"

// Request is the JSON body the web chat's completion endpoint takes. The
// bridge (Phase 5) sends it from the browser, where the login cookies live;
// this type pins down its shape.
type Request struct {
	ChatSessionID   string   `json:"chat_session_id"`
	ParentMessageID *int     `json:"parent_message_id"`
	Prompt          string   `json:"prompt"`
	RefFileIDs      []string `json:"ref_file_ids"`
	ThinkingEnabled bool     `json:"thinking_enabled"`
	SearchEnabled   bool     `json:"search_enabled"`
	ModelType       string   `json:"model_type"`
	Preempt         bool     `json:"preempt"`
	Action          *string  `json:"action"`
	ShumeiDeviceID  string   `json:"shumei_device_id,omitempty"`
}

// NewRequest builds the request for a Turn in the given chat session.
func NewRequest(sessionID string, t harness.Turn) Request {
	return Request{
		ChatSessionID:   sessionID,
		ParentMessageID: t.ParentID,
		Prompt:          t.Prompt,
		RefFileIDs:      []string{},
		ThinkingEnabled: t.Thinking,
		ModelType:       "default",
	}
}
