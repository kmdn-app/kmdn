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
	Type     string    `json:"type"` // text | tool | proposal | edit | file_op
	Text     string    `json:"text,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Label    string    `json:"label,omitempty"`
	CallID   string    `json:"call_id,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
	// edit (suggestions in a page) and file_op (a rename or delete to confirm)
	Path     string `json:"path,omitempty"`
	To       string `json:"to,omitempty"`
	Accepted bool   `json:"accepted,omitempty"`
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

// acceptedPrefix marks the message recording a confirmed proposal (its run id).
const acceptedPrefix = "accepted:"

func view(m Message) MessageView { return viewWith(m, nil) }

// viewWith marks proposals already confirmed.
func viewWith(m Message, accepted map[string]bool) MessageView {
	v := MessageView{ID: m.ID, Role: m.Role, AuthorID: m.AuthorID, AuthorName: m.AuthorName, Context: m.Context, RunID: m.RunID, CreatedAt: m.CreatedAt, Parts: []Part{}}
	for _, b := range m.Content {
		switch b.Type {
		case llm.BlockText:
			if b.Text != "" {
				v.Parts = append(v.Parts, Part{Type: "text", Text: b.Text})
			}
		case llm.BlockToolUse:
			var in struct {
				Path string `json:"path"`
				From string `json:"from"`
				To   string `json:"to"`
			}
			_ = json.Unmarshal(b.Input, &in)
			switch {
			case b.Name == proposeTool.Name:
				var p Proposal
				_ = json.Unmarshal(b.Input, &p)
				v.Parts = append(v.Parts, Part{Type: "proposal", Tool: b.Name, CallID: b.ID, Proposal: &p, Accepted: accepted[b.ID]})
				continue
			case b.Name == "edit_file" || b.Name == "create_file":
				v.Parts = append(v.Parts, Part{Type: "edit", Tool: b.Name, Label: toolLabel(b.Name, b.Input), CallID: b.ID, Path: in.Path})
				continue
			case fileOps[b.Name]:
				path := in.Path
				if b.Name == "rename_file" {
					path = in.From
				}
				v.Parts = append(v.Parts, Part{Type: "file_op", Tool: b.Name, Label: toolLabel(b.Name, b.Input), CallID: b.ID, Path: path, To: in.To, Accepted: accepted[b.ID]})
				continue
			}
			v.Parts = append(v.Parts, Part{Type: "tool", Tool: b.Name, Label: toolLabel(b.Name, b.Input), CallID: b.ID})
		}
	}
	return v
}

func itoa(n int) string { return strconv.Itoa(n) }
