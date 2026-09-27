package docengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dop251/goja"
)

// MaxUpdateBytes bounds Yjs inputs (a document's full state with history can
// be well above its markdown size).
const MaxUpdateBytes = 16 << 20

// ErrBadUpdate is returned for bytes that aren't a valid Yjs update.
var ErrBadUpdate = errors.New("docengine: invalid Yjs update")

func bin(v *vm, b []byte) goja.Value { return v.rt.ToValue(v.rt.NewArrayBuffer(b)) }

func bytesOf(val goja.Value) ([]byte, error) {
	ab, ok := val.Export().(goja.ArrayBuffer)
	if !ok {
		return nil, fmt.Errorf("docengine: expected an ArrayBuffer, got %T", val.Export())
	}
	return ab.Bytes(), nil
}

func (e *Engine) binCall(ctx context.Context, name string, size int, args func(v *vm) []goja.Value) ([]byte, error) {
	if size > MaxUpdateBytes {
		return nil, ErrTooLarge
	}
	return run(ctx, e, func(v *vm) ([]byte, error) {
		f, err := v.fn(name)
		if err != nil {
			return nil, err
		}
		out, err := f(goja.Undefined(), args(v)...)
		if err != nil {
			return nil, err
		}
		return bytesOf(out)
	})
}

// YFromMarkdown builds a new collaborative document for a page. It returns the
// state as a Yjs update written by clientID, and the source map used to keep
// untouched bytes when materializing.
func (e *Engine) YFromMarkdown(ctx context.Context, markdown string, clientID uint32) (update []byte, sourceMap string, err error) {
	if len(markdown) > e.opts.MaxInputBytes {
		return nil, "", ErrTooLarge
	}
	type res struct {
		u  []byte
		sm string
	}
	r, err := run(ctx, e, func(v *vm) (res, error) {
		f, err := v.fn("yFromMarkdown")
		if err != nil {
			return res{}, err
		}
		out, err := f(goja.Undefined(), v.rt.ToValue(markdown), v.rt.ToValue(clientID))
		if err != nil {
			return res{}, err
		}
		o := out.ToObject(v.rt)
		u, err := bytesOf(o.Get("update"))
		if err != nil {
			return res{}, err
		}
		return res{u, o.Get("sourceMap").String()}, nil
	})
	return r.u, r.sm, err
}

// YMaterialize serializes a document state to markdown.
func (e *Engine) YMaterialize(ctx context.Context, state []byte, sourceMap string) (string, error) {
	if len(state) > MaxUpdateBytes {
		return "", ErrTooLarge
	}
	return run(ctx, e, func(v *vm) (string, error) {
		f, err := v.fn("yMaterialize")
		if err != nil {
			return "", err
		}
		out, err := f(goja.Undefined(), bin(v, state), v.rt.ToValue(sourceMap))
		if err != nil {
			return "", err
		}
		return out.String(), nil
	})
}

// YMerge merges updates into one.
func (e *Engine) YMerge(ctx context.Context, updates [][]byte) ([]byte, error) {
	if len(updates) == 1 {
		return updates[0], nil
	}
	size := 0
	for _, u := range updates {
		size += len(u)
	}
	return e.binCall(ctx, "yMerge", size, func(v *vm) []goja.Value {
		arr := make([]any, len(updates))
		for i, u := range updates {
			arr[i] = v.rt.NewArrayBuffer(u)
		}
		return []goja.Value{v.rt.ToValue(arr)}
	})
}

// YDiff returns what a peer with state vector sv is missing from state.
func (e *Engine) YDiff(ctx context.Context, state, sv []byte) ([]byte, error) {
	return e.binCall(ctx, "yDiff", len(state), func(v *vm) []goja.Value { return []goja.Value{bin(v, state), bin(v, sv)} })
}

// YStateVector encodes the state vector of an update.
func (e *Engine) YStateVector(ctx context.Context, state []byte) ([]byte, error) {
	return e.binCall(ctx, "yStateVector", len(state), func(v *vm) []goja.Value { return []goja.Value{bin(v, state)} })
}

// YClients validates an update and returns the Yjs client ids that wrote it.
func (e *Engine) YClients(ctx context.Context, update []byte) ([]uint64, error) {
	if len(update) > MaxUpdateBytes {
		return nil, ErrTooLarge
	}
	ids, err := run(ctx, e, func(v *vm) ([]uint64, error) {
		f, err := v.fn("yClients")
		if err != nil {
			return nil, err
		}
		out, err := f(goja.Undefined(), bin(v, update))
		if err != nil {
			return nil, err
		}
		var ids []uint64
		if err := v.rt.ExportTo(out, &ids); err != nil {
			return nil, err
		}
		return ids, nil
	})
	if err != nil && !errors.Is(err, ErrTimeout) && !errors.Is(err, ErrTooLarge) && !errors.Is(err, context.Canceled) {
		return nil, fmt.Errorf("%w: %w", ErrBadUpdate, err)
	}
	return ids, err
}

// YApplyMarkdown returns the update that turns state into markdown, written by
// clientID: edits made outside an editor (assistant, updates from Published,
// checkpoint restore). An unchanged document yields an empty update (≤ 2 bytes).
func (e *Engine) YApplyMarkdown(ctx context.Context, state []byte, markdown string, clientID uint32) ([]byte, error) {
	if len(markdown) > e.opts.MaxInputBytes {
		return nil, ErrTooLarge
	}
	return e.binCall(ctx, "yApplyMarkdown", len(state), func(v *vm) []goja.Value {
		return []goja.Value{bin(v, state), v.rt.ToValue(markdown), v.rt.ToValue(clientID)}
	})
}

// Link is a link, image or reference definition found in a page.
type Link struct {
	Kind      string `json:"kind"` // link | image | definition
	URL       string `json:"url"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Line      int    `json:"line"`
	Bracketed bool   `json:"bracketed"`
}

// Heading is a heading and its anchor slug (deduplicated like GitHub).
type Heading struct {
	Text  string `json:"text"`
	Depth int    `json:"depth"`
	Slug  string `json:"slug"`
	Line  int    `json:"line"`
}

// Links extracts a page's links and headings.
func (e *Engine) Links(ctx context.Context, markdown string) ([]Link, []Heading, error) {
	if len(markdown) > e.opts.MaxInputBytes {
		return nil, nil, ErrTooLarge
	}
	s, err := e.call(ctx, func(v *vm) (goja.Value, error) {
		f, err := v.fn("links")
		if err != nil {
			return nil, err
		}
		return f(goja.Undefined(), v.rt.ToValue(markdown))
	})
	if err != nil {
		return nil, nil, err
	}
	var out struct {
		Links    []Link    `json:"links"`
		Headings []Heading `json:"headings"`
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, nil, err
	}
	return out.Links, out.Headings, nil
}

// RewriteLinks replaces link destinations (by exact destination) and keeps every other byte.
func (e *Engine) RewriteLinks(ctx context.Context, markdown string, replace map[string]string) (string, error) {
	if len(markdown) > e.opts.MaxInputBytes {
		return "", ErrTooLarge
	}
	b, err := json.Marshal(replace)
	if err != nil {
		return "", err
	}
	return e.call(ctx, func(v *vm) (goja.Value, error) {
		f, err := v.fn("rewriteLinks")
		if err != nil {
			return nil, err
		}
		return f(goja.Undefined(), v.rt.ToValue(markdown), v.rt.ToValue(string(b)))
	})
}

// YContributions counts, per Yjs client id, the content it wrote that still
// survives in the document.
func (e *Engine) YContributions(ctx context.Context, state []byte) (map[uint64]int, error) {
	if len(state) > MaxUpdateBytes {
		return nil, ErrTooLarge
	}
	s, err := run(ctx, e, func(v *vm) (string, error) {
		f, err := v.fn("yContributions")
		if err != nil {
			return "", err
		}
		out, err := f(goja.Undefined(), bin(v, state))
		if err != nil {
			return "", err
		}
		return out.String(), nil
	})
	if err != nil {
		return nil, err
	}
	var raw map[string]int
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	out := make(map[uint64]int, len(raw))
	for k, n := range raw {
		var id uint64
		if _, err := fmt.Sscan(k, &id); err == nil {
			out[id] = n
		}
	}
	return out, nil
}

// YSetMapEntry returns the update setting key in the document's map name to
// a JSON value (an empty value deletes it), written by clientID.
func (e *Engine) YSetMapEntry(ctx context.Context, state []byte, clientID uint32, name, key, valueJSON string) ([]byte, error) {
	return e.binCall(ctx, "ySetMapEntry", len(state), func(v *vm) []goja.Value {
		return []goja.Value{bin(v, state), v.rt.ToValue(clientID), v.rt.ToValue(name), v.rt.ToValue(key), v.rt.ToValue(valueJSON)}
	})
}
