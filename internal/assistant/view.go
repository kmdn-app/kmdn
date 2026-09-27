package assistant

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/kmdn-app/kmdn/internal/llm"
)

// Part is what the UI shows of a message: text, a tool's activity line, or
// a revision proposal card. Tool results stay server-side.
type Part struct {
	Type     string    `json:"type"` // text | tool | proposal
	Text     string    `json:"text,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Label    string    `json:"label,omitempty"`
	CallID   string    `json:"call_id,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
}

// MessageView is a message for the UI.
type MessageView struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	AuthorID   string    `json:"author_id,omitempty"`
	AuthorName string    `json:"author_name,omitempty"`
	Parts      []Part    `json:"parts"`
	Context    Context   `json:"context"`
	RunID      string    `json:"run_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func view(m Message) MessageView {
	v := MessageView{ID: m.ID, Role: m.Role, AuthorID: m.AuthorID, AuthorName: m.AuthorName, Context: m.Context, RunID: m.RunID, CreatedAt: m.CreatedAt, Parts: []Part{}}
	for _, b := range m.Content {
		switch b.Type {
		case llm.BlockText:
			if b.Text != "" {
				v.Parts = append(v.Parts, Part{Type: "text", Text: b.Text})
			}
		case llm.BlockToolUse:
			if b.Name == proposeTool.Name {
				var p Proposal
				_ = json.Unmarshal(b.Input, &p)
				v.Parts = append(v.Parts, Part{Type: "proposal", Tool: b.Name, CallID: b.ID, Proposal: &p})
				continue
			}
			v.Parts = append(v.Parts, Part{Type: "tool", Tool: b.Name, Label: toolLabel(b.Name, b.Input), CallID: b.ID})
		}
	}
	return v
}

func itoa(n int) string { return strconv.Itoa(n) }
