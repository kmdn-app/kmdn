// Package docengine runs the shared TypeScript document engine
// (packages/doc-engine, bundled to engine.js) inside Go with goja, so the
// server parses and serializes markdown with exactly the same code as the
// browser. See docs/specs/04-doc-engine.md#server-host-go--js-bridge.
//
// Runtimes are not goroutine-safe; Engine keeps a pool. Each call has a time
// budget enforced with goja's Interrupt. Parse results are cached by content
// hash because the same published blob is parsed for many revisions.
package docengine

import (
	"container/list"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// Bundle is the compiled engine (regenerate with `pnpm --filter @kmdn/doc-engine bundle`).
//
//go:embed engine.js
var Bundle string

// ErrTimeout is returned when a call exceeds its budget.
var ErrTimeout = errors.New("docengine: call exceeded its time budget")

// ErrTooLarge is returned for inputs over MaxInputBytes.
var ErrTooLarge = errors.New("docengine: input too large")

// ParseResult is the engine's parse output. Doc and SourceMap are JSON.
type ParseResult struct {
	Doc       json.RawMessage `json:"doc"`
	SourceMap json.RawMessage `json:"sourceMap"`
}

// Options configures an Engine.
type Options struct {
	Runtimes      int           // pool size; default min(GOMAXPROCS, 4)
	Timeout       time.Duration // per-call budget when ctx has no deadline; default 30s
	MaxInputBytes int           // default 1 MiB (larger files are source-only in the editor)
	CacheEntries  int           // parse cache size; default 256
}

type vm struct {
	rt        *goja.Runtime
	parse     goja.Callable
	serialize goja.Callable
}

// Engine is a pool of JS runtimes running the doc engine.
type Engine struct {
	opts    Options
	prog    *goja.Program
	pool    chan *vm
	version int

	cacheMu sync.Mutex
	cache   map[[32]byte]*list.Element
	lru     *list.List
}

type cacheEntry struct {
	key [32]byte
	res ParseResult
}

// New compiles the bundle and warms one runtime.
func New(o Options) (*Engine, error) {
	if o.Runtimes <= 0 {
		o.Runtimes = min(runtime.GOMAXPROCS(0), 4)
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxInputBytes <= 0 {
		o.MaxInputBytes = 1 << 20
	}
	if o.CacheEntries <= 0 {
		o.CacheEntries = 256
	}
	prog, err := goja.Compile("engine.js", Bundle, true)
	if err != nil {
		return nil, fmt.Errorf("docengine: compile: %w", err)
	}
	e := &Engine{opts: o, prog: prog, pool: make(chan *vm, o.Runtimes), cache: map[[32]byte]*list.Element{}, lru: list.New()}
	first, err := e.newVM()
	if err != nil {
		return nil, err
	}
	v, err := first.rt.RunString("kmdn.version()")
	if err != nil {
		return nil, err
	}
	e.version = int(v.ToInteger())
	e.pool <- first
	for i := 1; i < o.Runtimes; i++ {
		// Remaining runtimes are created lazily on first use.
		e.pool <- nil
	}
	return e, nil
}

func (e *Engine) newVM() (*vm, error) {
	rt := goja.New()
	if _, err := rt.RunProgram(e.prog); err != nil {
		return nil, fmt.Errorf("docengine: load: %w", err)
	}
	k := rt.Get("kmdn").ToObject(rt)
	parse, ok1 := goja.AssertFunction(k.Get("parse"))
	ser, ok2 := goja.AssertFunction(k.Get("serialize"))
	if !ok1 || !ok2 {
		return nil, errors.New("docengine: bundle does not export kmdn.parse/serialize")
	}
	return &vm{rt: rt, parse: parse, serialize: ser}, nil
}

// Version is the engine version stamped in the bundle.
func (e *Engine) Version() int { return e.version }

func (e *Engine) call(ctx context.Context, f func(*vm) (goja.Value, error)) (string, error) {
	var v *vm
	select {
	case v = <-e.pool:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if v == nil {
		nv, err := e.newVM()
		if err != nil {
			e.pool <- nil
			return "", err
		}
		v = nv
	}
	budget := e.opts.Timeout
	if dl, ok := ctx.Deadline(); ok {
		budget = time.Until(dl)
	}
	timer := time.AfterFunc(budget, func() { v.rt.Interrupt(ErrTimeout) })
	out, err := f(v)
	stopped := timer.Stop()
	if !stopped {
		// The interrupt fired (or is firing): the runtime may be mid-call, so
		// discard it rather than returning it to the pool.
		v.rt.ClearInterrupt()
		e.pool <- nil
		if err == nil {
			err = ErrTimeout
		}
	} else {
		e.pool <- v
	}
	if err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			return "", ErrTimeout
		}
		var ex *goja.Exception
		if errors.As(err, &ex) {
			return "", fmt.Errorf("docengine: %s", ex.Value().String())
		}
		return "", err
	}
	return out.String(), nil
}

// Parse parses markdown into the document model and source map.
func (e *Engine) Parse(ctx context.Context, markdown string) (ParseResult, error) {
	if len(markdown) > e.opts.MaxInputBytes {
		return ParseResult{}, ErrTooLarge
	}
	key := sha256.Sum256([]byte(markdown))
	if r, ok := e.cached(key); ok {
		return r, nil
	}
	s, err := e.call(ctx, func(v *vm) (goja.Value, error) {
		return v.parse(goja.Undefined(), v.rt.ToValue(markdown))
	})
	if err != nil {
		return ParseResult{}, err
	}
	var r ParseResult
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return r, fmt.Errorf("docengine: decode parse result: %w", err)
	}
	e.store(key, r)
	return r, nil
}

// Serialize turns a document (and optional source map) back into markdown.
func (e *Engine) Serialize(ctx context.Context, doc, sourceMap json.RawMessage) (string, error) {
	if len(doc)+len(sourceMap) > 4*e.opts.MaxInputBytes {
		return "", ErrTooLarge
	}
	return e.call(ctx, func(v *vm) (goja.Value, error) {
		sm := ""
		if len(sourceMap) > 0 && string(sourceMap) != "null" {
			sm = string(sourceMap)
		}
		return v.serialize(goja.Undefined(), v.rt.ToValue(string(doc)), v.rt.ToValue(sm))
	})
}

func (e *Engine) cached(key [32]byte) (ParseResult, bool) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	el, ok := e.cache[key]
	if !ok {
		return ParseResult{}, false
	}
	e.lru.MoveToFront(el)
	return el.Value.(*cacheEntry).res, true
}

func (e *Engine) store(key [32]byte, r ParseResult) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	if el, ok := e.cache[key]; ok {
		e.lru.MoveToFront(el)
		return
	}
	e.cache[key] = e.lru.PushFront(&cacheEntry{key: key, res: r})
	for e.lru.Len() > e.opts.CacheEntries {
		old := e.lru.Back()
		e.lru.Remove(old)
		delete(e.cache, old.Value.(*cacheEntry).key)
	}
}
