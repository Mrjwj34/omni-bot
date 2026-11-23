package kernel

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

var (
	ErrBusClosed   = errors.New("kernel: single-flight bus is closed")
	ErrEmptyRoomID = errors.New("kernel: room_id is required")
	ErrNilRunFunc  = errors.New("kernel: run function is required")
)

// RunFunc executes a single serialized Run for one room.
type RunFunc func(ctx context.Context, ev TriggerEvent) error

// BusOptions configures SingleFlightBus scheduling behavior.
type BusOptions struct {
	DebounceWindow time.Duration
	Logger         *slog.Logger
	OnError        func(roomID string, err error)
}

type roomSlot struct {
	running        bool
	pending        bool
	pendingEvent   TriggerEvent
	debounceTimer  *time.Timer
	debounceEvent  TriggerEvent
	hasDebounce    bool
	currentWaiters []chan error
	pendingWaiters []chan error
}

// SingleFlightBus enforces at most one active Run per room_id while allowing independent rooms
// to run concurrently. Incoming triggers during an active Run coalesce into one follow-up micro-batch.
type SingleFlightBus struct {
	runFn          RunFunc
	debounceWindow time.Duration
	logger         *slog.Logger
	onError        func(roomID string, err error)

	rootCtx    context.Context
	rootCancel context.CancelFunc

	// mu protects rooms and closed only; never held while invoking runFn.
	mu     sync.Mutex
	closed bool
	rooms  map[string]*roomSlot
	wg     sync.WaitGroup
}

// NewSingleFlightBus creates a SingleFlightBus wired to runFn.
func NewSingleFlightBus(runFn RunFunc, opts BusOptions) (*SingleFlightBus, error) {
	if runFn == nil {
		return nil, ErrNilRunFunc
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SingleFlightBus{
		runFn:          runFn,
		debounceWindow: opts.DebounceWindow,
		logger:         logger.With("component", "kernel.bus"),
		onError:        opts.OnError,
		rootCtx:        ctx,
		rootCancel:     cancel,
		rooms:          make(map[string]*roomSlot),
	}, nil
}

// SetDebounceWindow updates the debounce window dynamically without disrupting active runs.
func (b *SingleFlightBus) SetDebounceWindow(d time.Duration) {
	if d < 0 {
		d = 0
	}
	b.mu.Lock()
	b.debounceWindow = d
	b.mu.Unlock()
}

// Submit schedules ev on the room's single-flight lane without blocking on Run execution.
func (b *SingleFlightBus) Submit(ev TriggerEvent) error {
	_, err := b.submit(ev, false)
	return err
}

// SubmitAndWait schedules ev and waits until the Run covering ev completes or ctx is canceled.
func (b *SingleFlightBus) SubmitAndWait(ctx context.Context, ev TriggerEvent) error {
	ch, err := b.submit(ev, true)
	if err != nil {
		return err
	}
	select {
	case runErr := <-ch:
		return runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *SingleFlightBus) submit(ev TriggerEvent, wantWaiter bool) (chan error, error) {
	ev.RoomID = strings.TrimSpace(ev.RoomID)
	if ev.RoomID == "" {
		return nil, ErrEmptyRoomID
	}
	if ev.Source == "" {
		ev.Source = TriggerMessage
	}
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = time.Now()
	}

	var waiter chan error
	if wantWaiter {
		waiter = make(chan error, 1)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrBusClosed
	}

	slot := b.rooms[ev.RoomID]
	if slot == nil {
		slot = &roomSlot{}
		b.rooms[ev.RoomID] = slot
	}

	if slot.running {
		if slot.pending {
			slot.pendingEvent = coalesceTrigger(slot.pendingEvent, ev)
		} else {
			slot.pending = true
			slot.pendingEvent = ev
		}
		if waiter != nil {
			slot.pendingWaiters = append(slot.pendingWaiters, waiter)
		}
		b.mu.Unlock()
		return waiter, nil
	}

	// Debounce rapid group messages when idle; BypassGate or non-message triggers fire immediately.
	if b.debounceWindow > 0 && ev.Source == TriggerMessage && !ev.BypassGate {
		if slot.hasDebounce {
			slot.debounceEvent = coalesceTrigger(slot.debounceEvent, ev)
			slot.debounceTimer.Reset(b.debounceWindow)
		} else {
			slot.hasDebounce = true
			slot.debounceEvent = ev
			roomID := ev.RoomID
			slot.debounceTimer = time.AfterFunc(b.debounceWindow, func() {
				b.fireDebounce(roomID)
			})
		}
		if waiter != nil {
			slot.pendingWaiters = append(slot.pendingWaiters, waiter)
		}
		b.mu.Unlock()
		return waiter, nil
	}

	// Cancel any pending debounce timer and merge its event into this immediate launch.
	if slot.hasDebounce {
		slot.debounceTimer.Stop()
		ev = coalesceTrigger(slot.debounceEvent, ev)
		slot.debounceTimer = nil
		slot.hasDebounce = false
	}

	slot.running = true
	slot.currentWaiters = slot.pendingWaiters
	slot.pendingWaiters = nil
	if waiter != nil {
		slot.currentWaiters = append(slot.currentWaiters, waiter)
	}
	b.wg.Add(1)
	b.mu.Unlock()

	go b.runRoomLoop(ev.RoomID, ev)
	return waiter, nil
}

func (b *SingleFlightBus) fireDebounce(roomID string) {
	b.mu.Lock()
	slot := b.rooms[roomID]
	if b.closed || slot == nil || !slot.hasDebounce {
		b.mu.Unlock()
		return
	}
	ev := slot.debounceEvent
	slot.debounceTimer = nil
	slot.hasDebounce = false

	if slot.running {
		if slot.pending {
			slot.pendingEvent = coalesceTrigger(slot.pendingEvent, ev)
		} else {
			slot.pending = true
			slot.pendingEvent = ev
		}
		b.mu.Unlock()
		return
	}

	slot.running = true
	slot.currentWaiters = slot.pendingWaiters
	slot.pendingWaiters = nil
	b.wg.Add(1)
	b.mu.Unlock()

	go b.runRoomLoop(roomID, ev)
}

func (b *SingleFlightBus) runRoomLoop(roomID string, initial TriggerEvent) {
	defer b.wg.Done()
	ev := initial

	for {
		// Execute strictly outside b.mu so concurrent submissions and other rooms never block.
		err := b.runFn(b.rootCtx, ev)
		if err != nil && !errors.Is(err, context.Canceled) {
			b.logger.Warn("room run failed",
				slog.String("room_id", roomID),
				slog.String("source", string(ev.Source)),
				slog.String("err", err.Error()),
			)
			if b.onError != nil {
				b.onError(roomID, err)
			}
		}

		b.mu.Lock()
		slot := b.rooms[roomID]
		doneWaiters := slot.currentWaiters
		slot.currentWaiters = nil

		for _, ch := range doneWaiters {
			ch <- err
			close(ch)
		}

		if b.closed || !slot.pending {
			slot.running = false
			if slot.pending {
				slot.pending = false
				for _, ch := range slot.pendingWaiters {
					ch <- ErrBusClosed
					close(ch)
				}
				slot.pendingWaiters = nil
			}
			if !slot.hasDebounce {
				delete(b.rooms, roomID)
			}
			b.mu.Unlock()
			return
		}

		// Queue length is strictly 1 (latest-state coalescing): if messages arrived while this Run
		// was executing, re-arm the debounce window for remaining time so bursts (e.g. msg3, msg4)
		// settle into a single latest checkpoint instead of firing stale back-to-back runs.
		ev = slot.pendingEvent
		slot.pending = false
		slot.pendingEvent = TriggerEvent{}
		pendingWaiters := slot.pendingWaiters
		slot.pendingWaiters = nil

		if b.debounceWindow > 0 && ev.Source == TriggerMessage && !ev.BypassGate {
			waitFor := b.debounceWindow - time.Since(ev.ReceivedAt)
			if waitFor > 25*time.Millisecond {
				slot.running = false
				slot.hasDebounce = true
				slot.debounceEvent = ev
				slot.pendingWaiters = pendingWaiters
				rid := roomID
				slot.debounceTimer = time.AfterFunc(waitFor, func() {
					b.fireDebounce(rid)
				})
				b.mu.Unlock()
				return
			}
		}

		slot.currentWaiters = pendingWaiters
		b.mu.Unlock()
	}
}

// ActiveRooms returns the number of rooms currently executing a Run.
func (b *SingleFlightBus) ActiveRooms() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, slot := range b.rooms {
		if slot.running {
			n++
		}
	}
	return n
}

// Close cancels the bus context, stops pending debounce timers, and waits for in-flight runs to exit.
func (b *SingleFlightBus) Close(ctx context.Context) error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		for roomID, slot := range b.rooms {
			if slot.hasDebounce && slot.debounceTimer != nil {
				slot.debounceTimer.Stop()
				slot.debounceTimer = nil
				slot.hasDebounce = false
			}
			if !slot.running {
				for _, ch := range slot.pendingWaiters {
					ch <- ErrBusClosed
					close(ch)
				}
				slot.pendingWaiters = nil
				delete(b.rooms, roomID)
			}
		}
	}
	b.mu.Unlock()

	b.rootCancel()

	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func coalesceTrigger(prev, next TriggerEvent) TriggerEvent {
	merged := next
	if prev.RoomID != "" {
		merged.RoomID = prev.RoomID
	}
	if merged.ChannelID == "" {
		merged.ChannelID = prev.ChannelID
	}
	merged.BypassGate = prev.BypassGate || next.BypassGate
	if prev.Source != TriggerMessage && next.Source == TriggerMessage {
		merged.Source = prev.Source
	}
	if merged.Payload == nil {
		merged.Payload = prev.Payload
	}
	if next.ReceivedAt.After(prev.ReceivedAt) {
		merged.ReceivedAt = next.ReceivedAt
	} else if !prev.ReceivedAt.IsZero() {
		merged.ReceivedAt = prev.ReceivedAt
	}
	return merged
}
