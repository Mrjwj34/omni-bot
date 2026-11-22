package kernel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleFlightBus_PerRoomSerializationAndMicroBatchCoalescing(t *testing.T) {
	startedFirst := make(chan struct{})
	releaseFirst := make(chan struct{})

	var active atomic.Int32
	var maxActive atomic.Int32
	var runs atomic.Int32

	bus, err := NewSingleFlightBus(func(ctx context.Context, ev TriggerEvent) error {
		cur := active.Add(1)
		for {
			prev := maxActive.Load()
			if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer active.Add(-1)

		n := runs.Add(1)
		if n == 1 {
			close(startedFirst)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, BusOptions{})
	if err != nil {
		t.Fatalf("NewSingleFlightBus: %v", err)
	}
	defer func() { _ = bus.Close(t.Context()) }()

	if err := bus.Submit(TriggerEvent{Source: TriggerMessage, RoomID: "room-a"}); err != nil {
		t.Fatalf("submit initial: %v", err)
	}
	<-startedFirst

	// Submit 5 concurrent triggers while run #1 is active; they must coalesce into exactly 1 follow-up run.
	waiters := make([]chan error, 0, 5)
	for i := range 5 {
		ch := make(chan error, 1)
		waiters = append(waiters, ch)
		go func(idx int, done chan error) {
			done <- bus.SubmitAndWait(t.Context(), TriggerEvent{
				Source:     TriggerMessage,
				RoomID:     "room-a",
				BypassGate: idx == 3,
			})
		}(i, ch)
	}

	// Give goroutines a moment to enqueue while run #1 holds the lane.
	time.Sleep(30 * time.Millisecond)
	close(releaseFirst)

	for _, ch := range waiters {
		if err := <-ch; err != nil {
			t.Fatalf("waiter error: %v", err)
		}
	}

	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent runs for same room = %d, want 1", got)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("total runs after coalescing 5 concurrent triggers = %d, want 2", got)
	}
}

func TestSingleFlightBus_CrossRoomParallelism(t *testing.T) {
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	var started atomic.Int32

	bus, err := NewSingleFlightBus(func(ctx context.Context, ev TriggerEvent) error {
		if started.Add(1) == 2 {
			close(bothStarted)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, BusOptions{})
	if err != nil {
		t.Fatalf("NewSingleFlightBus: %v", err)
	}
	defer func() { _ = bus.Close(t.Context()) }()

	if err := bus.Submit(TriggerEvent{Source: TriggerMessage, RoomID: "room-1"}); err != nil {
		t.Fatalf("submit room-1: %v", err)
	}
	if err := bus.Submit(TriggerEvent{Source: TriggerMessage, RoomID: "room-2"}); err != nil {
		t.Fatalf("submit room-2: %v", err)
	}

	select {
	case <-bothStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected room-1 and room-2 to execute concurrently")
	}
	close(release)
}

func TestSingleFlightBus_DebounceAndBypass(t *testing.T) {
	var (
		mu       sync.Mutex
		observed []TriggerEvent
	)

	bus, err := NewSingleFlightBus(func(ctx context.Context, ev TriggerEvent) error {
		mu.Lock()
		observed = append(observed, ev)
		mu.Unlock()
		return nil
	}, BusOptions{DebounceWindow: 60 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewSingleFlightBus: %v", err)
	}
	defer func() { _ = bus.Close(t.Context()) }()

	// Three rapid messages within debounce window should produce one single run.
	for range 3 {
		if err := bus.Submit(TriggerEvent{Source: TriggerMessage, RoomID: "room-deb"}); err != nil {
			t.Fatalf("submit debounced: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	countAfterDebounce := len(observed)
	mu.Unlock()
	if countAfterDebounce != 1 {
		t.Fatalf("runs after debounced burst = %d, want 1", countAfterDebounce)
	}

	// A BypassGate event must flush immediately without waiting for debounce.
	t0 := time.Now()
	if err := bus.SubmitAndWait(t.Context(), TriggerEvent{
		Source:     TriggerWebUI,
		RoomID:     "room-deb",
		BypassGate: true,
	}); err != nil {
		t.Fatalf("submit bypass: %v", err)
	}
	if elapsed := time.Since(t0); elapsed >= 50*time.Millisecond {
		t.Fatalf("bypass trigger took %v, expected immediate dispatch", elapsed)
	}
}

func TestSingleFlightBus_CloseCancelsInFlightAndRejectsNew(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})

	bus, err := NewSingleFlightBus(func(ctx context.Context, ev TriggerEvent) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}, BusOptions{})
	if err != nil {
		t.Fatalf("NewSingleFlightBus: %v", err)
	}

	if err := bus.Submit(TriggerEvent{Source: TriggerScheduled, RoomID: "room-cancel"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-entered

	closeCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := bus.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-canceled:
	default:
		t.Fatal("in-flight run did not observe cancellation")
	}

	if err := bus.Submit(TriggerEvent{Source: TriggerMessage, RoomID: "room-cancel"}); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("Submit after Close = %v, want ErrBusClosed", err)
	}
}

func TestHookChain_DeterministicContributorOrderingAndFreeze(t *testing.T) {
	chain := NewHookChain()
	var order []string

	chain.AddContributor(ContextContributorFunc{
		HookName: "dynamic_b",
		Order:    20,
		Fn: func(_ context.Context, _ *RunContext, draft *ContextDraft) error {
			order = append(order, "dynamic_b")
			draft.AddDynamic("B")
			return nil
		},
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "static_a",
		Order:    10,
		Fn: func(_ context.Context, _ *RunContext, draft *ContextDraft) error {
			order = append(order, "static_a")
			draft.AddStatic("A")
			return nil
		},
	})
	chain.AddContributor(ContextContributorFunc{
		HookName: "dynamic_a",
		Order:    20,
		Fn: func(_ context.Context, _ *RunContext, draft *ContextDraft) error {
			order = append(order, "dynamic_a")
			draft.AddDynamic("A2")
			return nil
		},
	})

	rc := &RunContext{RoomID: "room-1"}
	spec, err := chain.RunAssemble(t.Context(), rc, &ContextDraft{MaxSteps: 10})
	if err != nil {
		t.Fatalf("RunAssemble: %v", err)
	}
	wantOrder := []string{"static_a", "dynamic_a", "dynamic_b"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order[%d] = %q, want %q", i, order[i], wantOrder[i])
		}
	}
	if spec.SystemInstruction != "A" {
		t.Fatalf("SystemInstruction = %q, want A", spec.SystemInstruction)
	}
	if spec.MaxSteps != MaxAgentSteps {
		t.Fatalf("MaxSteps = %d, want capped %d", spec.MaxSteps, MaxAgentSteps)
	}
	if len(spec.InitialMessages) != 1 || spec.InitialMessages[0].Parts[0].Text != "A2\n\nB" {
		t.Fatalf("unexpected InitialMessages: %+v", spec.InitialMessages)
	}
}
