package kernel

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestRequiredResponseEventsByOperation(t *testing.T) {
	toolRequest := normalize.Request{
		Operation: normalize.Operation("example.jobs.create"), OperationContractVersion: 2,
		Tools: []normalize.Tool{{Name: "lookup"}},
	}
	thinkingRequest := normalize.Request{
		Operation: normalize.Operation("example.jobs.create"), OperationContractVersion: 2,
		Thinking: normalize.ThinkingIntent{Mode: "enabled"},
	}

	tests := []struct {
		name    string
		request normalize.Request
		want    []ResponseEventKind
		unwant  []ResponseEventKind
	}{
		{
			name:    "non-chat operation requires completion without text",
			request: normalize.Request{Operation: normalize.Operation("example.jobs.create"), OperationContractVersion: 2},
			want:    []ResponseEventKind{EventResponseComplete},
			unwant:  []ResponseEventKind{EventTextDelta},
		},
		{
			name:    "chat generation requires text and completion",
			request: normalize.Request{Operation: normalize.OperationChatGenerate},
			want:    []ResponseEventKind{EventTextDelta, EventResponseComplete},
		},
		{
			name:    "tools are required for either operation",
			request: toolRequest,
			want:    []ResponseEventKind{EventToolCallDelta, EventResponseComplete},
			unwant:  []ResponseEventKind{EventTextDelta},
		},
		{
			name: "thinking is required for either operation",
			request: normalize.Request{
				Operation: normalize.OperationChatGenerate,
				Thinking:  normalize.ThinkingIntent{Mode: "enabled"},
			},
			want: []ResponseEventKind{EventTextDelta, EventThinkingDelta, EventResponseComplete},
		},
		{
			name:    "non-chat thinking is required without text",
			request: thinkingRequest,
			want:    []ResponseEventKind{EventThinkingDelta, EventResponseComplete},
			unwant:  []ResponseEventKind{EventTextDelta},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RequiredResponseEvents(tt.request)
			for _, event := range tt.want {
				if !containsResponseEvent(got, event) {
					t.Errorf("RequiredResponseEvents() = %v, missing %q", got, event)
				}
			}
			for _, event := range tt.unwant {
				if containsResponseEvent(got, event) {
					t.Errorf("RequiredResponseEvents() = %v, unexpectedly contains %q", got, event)
				}
			}
		})
	}
}

func containsResponseEvent(events []ResponseEventKind, want ResponseEventKind) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}
