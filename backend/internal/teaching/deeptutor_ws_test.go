package teaching

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProgressFramesExposeStagesWithoutPrivateContent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, kind, state, want string }{
		{"retrieving", "rag_retrieval", "running", "正在检索相关资料…"},
		{"retrieved", "rag_retrieval", "complete", "资料检索完成，正在准备回答…"},
		{"retrieval failed", "rag_retrieval", "error", "扩展检索暂不可用，正在结合已有资料回答…"},
		{"generating", "agent_loop_round", "running", "正在生成回答…"},
		{"unknown", "internal_tool", "running", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []Event
			frame := dtFrame{raw: map[string]any{"type": "progress", "content": "PRIVATE_PROMPT", "metadata": map[string]any{"call_kind": tc.kind, "call_state": tc.state, "query": "PRIVATE_PROMPT"}}}
			terminal, err := consumeFrame(&Result{}, frame, func(e Event) error { events = append(events, e); return nil })
			if err != nil || terminal {
				t.Fatalf("progress terminated turn: %v", err)
			}
			if tc.want == "" {
				if len(events) != 0 {
					t.Fatal(events)
				}
				return
			}
			if len(events) != 1 || events[0].Type != "progress" || events[0].Payload["message"] != tc.want {
				t.Fatalf("events=%+v", events)
			}
			data, _ := json.Marshal(events)
			if strings.Contains(string(data), "PRIVATE_PROMPT") {
				t.Fatal("private progress leaked")
			}
		})
	}
}
