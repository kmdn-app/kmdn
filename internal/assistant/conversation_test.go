package assistant

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/llm"
)

func person(name, text string) Message {
	return Message{Role: llm.RoleUser, AuthorName: name, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}
}

func reply(run, text string) Message {
	return Message{Role: llm.RoleAssistant, RunID: run, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}
}

func toolUse(run, id string) Message {
	return Message{Role: llm.RoleAssistant, RunID: run, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: "search"}}}
}

func toolResult(run, id string) Message {
	return Message{Role: llm.RoleUser, RunID: run, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: id, Content: "found"}}}
}

// shape describes a history: people's text, "use:id", "result:id" and assistant text.
func shape(msgs []llm.Message) string {
	var parts []string
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				parts = append(parts, "use:"+b.ID)
			case llm.BlockToolResult:
				parts = append(parts, "result:"+b.ToolUseID)
			default:
				parts = append(parts, b.Text)
			}
		}
	}
	return strings.Join(parts, " | ")
}

func TestConversationMovesMidRunPrompts(t *testing.T) {
	msgs := []Message{
		person("Sam", "rename the page"),
		toolUse("r1", "a"),
		person("Luis", "also fix the typo"), // posted while search ran
		toolResult("r1", "a"),
		toolUse("r1", "b"),
		person("Ana", "and the title"),
		toolResult("r1", "b"),
		reply("r1", "renamed"),
	}
	got := shape(conversation(msgs, true))
	want := "Sam: rename the page | use:a | result:a | use:b | result:b | renamed | Luis: also fix the typo | Ana: and the title"
	if got != want {
		t.Fatalf("history:\n got %s\nwant %s", got, want)
	}
	// Storage order is untouched.
	if msgs[2].AuthorName != "Luis" || msgs[5].AuthorName != "Ana" {
		t.Fatalf("reordered in place: %+v", msgs)
	}
}

func TestConversationPromptBeforeRunReplies(t *testing.T) {
	// Luis wrote while run r1 waited for its first reply: r1 never saw it,
	// so it goes after r1 and is the last message the model sees.
	msgs := []Message{
		person("Sam", "one"),
		person("Luis", "two"),
		toolUse("r1", "a"),
		toolResult("r1", "a"),
		reply("r1", "answer one"),
	}
	got := shape(conversation(msgs, true))
	want := "Sam: one | use:a | result:a | answer one | Luis: two"
	if got != want {
		t.Fatalf("history:\n got %s\nwant %s", got, want)
	}
}

func TestConversationWindow(t *testing.T) {
	var msgs []Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, person("Sam", fmt.Sprintf("q%d", i)))
		run := fmt.Sprintf("r%d", i)
		// Tool-heavy runs: 1 + 2*6 + 1 messages each.
		for j := 0; j < 6; j++ {
			id := fmt.Sprintf("%d-%d", i, j)
			msgs = append(msgs, toolUse(run, id), toolResult(run, id))
		}
		msgs = append(msgs, reply(run, fmt.Sprintf("a%d", i)))
	}
	msgs = append(msgs, person("Luis", "last"))

	// 8 turns of 14 messages is more than HistoryMessages: trimmed to the
	// last 80, then to the first person's message.
	h := conversation(msgs, false)
	if len(h) > HistoryMessages || h[0].Content[0].Text != "q7" || h[len(h)-1].Content[0].Text != "last" {
		t.Fatalf("window: %d messages, %s … %s", len(h), shape(h[:1]), shape(h[len(h)-1:]))
	}
	checkPairs(t, h)

	// Short turns: the window starts at the 8th latest prompt.
	var short []Message
	for i := 0; i < 20; i++ {
		short = append(short, person("Sam", fmt.Sprintf("q%d", i)), reply(fmt.Sprintf("r%d", i), fmt.Sprintf("a%d", i)))
	}
	h = conversation(short, false)
	if len(h) != 16 || h[0].Content[0].Text != "q12" {
		t.Fatalf("short window: %d messages from %s", len(h), shape(h[:1]))
	}

	// A prompt posted mid-run can't become the cut point inside a tool pair.
	mixed := []Message{person("Sam", "old")}
	for i := 0; i < 40; i++ {
		id := fmt.Sprint(i)
		mixed = append(mixed, toolUse("big", id))
		if i == 30 {
			mixed = append(mixed, person("Luis", "mid"))
		}
		mixed = append(mixed, toolResult("big", id))
	}
	mixed = append(mixed, reply("big", "done"))
	h = conversation(mixed, false)
	checkPairs(t, h)
	if h[len(h)-1].Content[0].Text != "mid" {
		t.Fatalf("mid-run prompt not last: %s", shape(h[len(h)-1:]))
	}
}

// checkPairs fails if a tool result doesn't directly follow its call.
func checkPairs(t *testing.T, h []llm.Message) {
	t.Helper()
	for i, m := range h {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			ok := i > 0
			if ok {
				ok = false
				for _, pb := range h[i-1].Content {
					ok = ok || (pb.Type == llm.BlockToolUse && pb.ID == b.ToolUseID)
				}
			}
			if !ok {
				t.Fatalf("result %s at %d doesn't follow its call: %s", b.ToolUseID, i, shape(h))
			}
		}
	}
}
