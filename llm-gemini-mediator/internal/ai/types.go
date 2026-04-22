package ai

type ChatRequest struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

type ChatResponse struct {
	Answer       string `json:"answer"`
	Explanations string `json:"explanations"`
	Status       string `json:"status"` // Essential for the success/error tagging
}