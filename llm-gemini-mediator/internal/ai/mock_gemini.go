package ai

import (
	"context"

	"google.golang.org/genai"
)

type MockGemini struct {
    History []string
    MockError    error
}

func (m *MockGemini) SendMessage(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error) {
    if m.MockError != nil {
        return nil, m.MockError
    }

    var currentRequest string
    for _, p := range parts {
        currentRequest = p.Text
    }

    m.History = append(m.History, "User: "+currentRequest)

    // fullTranscript := strings.Join(m.History, "\n")
    // mockText := "Mock Response to your request. Full history so far:\n" + fullTranscript

    m.History = append(m.History, "AI: " + currentRequest)

    return &genai.GenerateContentResponse{
        Candidates: []*genai.Candidate{
            {
                Content: &genai.Content{
                    Parts: []*genai.Part{
                        {
                            //Text: mockText,
                            Text: "AI: "+currentRequest,
                        },
                    },
                },
            },
        },
    }, nil
}