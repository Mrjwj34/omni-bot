package kernel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
)

// GateHook evaluates whether a trigger should proceed to a full Agent Run and updates continuous state (e.g. PAD).
type GateHook interface {
	Name() string
	OnGate(ctx context.Context, rc *RunContext) error
}

// ContextContributor injects static or dynamic prompt fragments during OnAssemble before RunSpec freezes.
type ContextContributor interface {
	Name() string
	Priority() int
	Contribute(ctx context.Context, rc *RunContext, draft *ContextDraft) error
}

// PostRunHook handles outbound delivery, trace persistence, UI broadcasts, and non-blocking background jobs.
type PostRunHook interface {
	Name() string
	OnPostRun(ctx context.Context, rc *RunContext) error
}

// GateHookFunc adapts a function into a GateHook.
type GateHookFunc struct {
	HookName string
	Fn       func(ctx context.Context, rc *RunContext) error
}

func (f GateHookFunc) Name() string { return f.HookName }

func (f GateHookFunc) OnGate(ctx context.Context, rc *RunContext) error {
	if f.Fn == nil {
		return nil
	}
	return f.Fn(ctx, rc)
}

// ContextContributorFunc adapts a function into a ContextContributor.
type ContextContributorFunc struct {
	HookName string
	Order    int
	Fn       func(ctx context.Context, rc *RunContext, draft *ContextDraft) error
}

func (f ContextContributorFunc) Name() string  { return f.HookName }
func (f ContextContributorFunc) Priority() int { return f.Order }

func (f ContextContributorFunc) Contribute(ctx context.Context, rc *RunContext, draft *ContextDraft) error {
	if f.Fn == nil {
		return nil
	}
	return f.Fn(ctx, rc, draft)
}

// PostRunHookFunc adapts a function into a PostRunHook.
type PostRunHookFunc struct {
	HookName string
	Fn       func(ctx context.Context, rc *RunContext) error
}

func (f PostRunHookFunc) Name() string { return f.HookName }

func (f PostRunHookFunc) OnPostRun(ctx context.Context, rc *RunContext) error {
	if f.Fn == nil {
		return nil
	}
	return f.Fn(ctx, rc)
}

// HookChain holds the registered 3-stage pipeline hooks and executes them in deterministic order.
type HookChain struct {
	mu           sync.RWMutex
	gates        []GateHook
	contributors []ContextContributor
	postRuns     []PostRunHook
}

func NewHookChain() *HookChain {
	return &HookChain{}
}

func (c *HookChain) AddGate(h GateHook) {
	if h == nil {
		return
	}
	c.mu.Lock()
	c.gates = append(c.gates, h)
	c.mu.Unlock()
}

func (c *HookChain) AddContributor(h ContextContributor) {
	if h == nil {
		return
	}
	c.mu.Lock()
	c.contributors = append(c.contributors, h)
	slices.SortStableFunc(c.contributors, func(a, b ContextContributor) int {
		return cmp.Or(
			cmp.Compare(a.Priority(), b.Priority()),
			cmp.Compare(a.Name(), b.Name()),
		)
	})
	c.mu.Unlock()
}

func (c *HookChain) AddPostRun(h PostRunHook) {
	if h == nil {
		return
	}
	c.mu.Lock()
	c.postRuns = append(c.postRuns, h)
	c.mu.Unlock()
}

// RunGate executes all registered GateHooks in registration order without holding locks.
func (c *HookChain) RunGate(ctx context.Context, rc *RunContext) error {
	c.mu.RLock()
	hooks := slices.Clone(c.gates)
	c.mu.RUnlock()

	for _, h := range hooks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := h.OnGate(ctx, rc); err != nil {
			return fmt.Errorf("kernel: gate hook %q: %w", h.Name(), err)
		}
	}
	return nil
}

// RunAssemble executes all registered ContextContributors in deterministic priority order
// and freezes the resulting ContextDraft into an immutable RunSpec.
func (c *HookChain) RunAssemble(ctx context.Context, rc *RunContext, draft *ContextDraft) (*RunSpec, error) {
	if draft == nil {
		draft = &ContextDraft{}
	}
	c.mu.RLock()
	contribs := slices.Clone(c.contributors)
	c.mu.RUnlock()

	for _, h := range contribs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := h.Contribute(ctx, rc, draft); err != nil {
			return nil, fmt.Errorf("kernel: context contributor %q: %w", h.Name(), err)
		}
	}
	spec := draft.Freeze()
	rc.Spec = spec
	return spec, nil
}

// RunPostRun executes all registered PostRunHooks in registration order.
func (c *HookChain) RunPostRun(ctx context.Context, rc *RunContext) error {
	c.mu.RLock()
	hooks := slices.Clone(c.postRuns)
	c.mu.RUnlock()

	for _, h := range hooks {
		if err := h.OnPostRun(ctx, rc); err != nil {
			return fmt.Errorf("kernel: post-run hook %q: %w", h.Name(), err)
		}
	}
	return nil
}
