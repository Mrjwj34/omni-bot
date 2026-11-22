package channel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/protocol"
)

const (
	defaultRPCTimeout   = 15 * time.Second
	defaultWriteTimeout = 10 * time.Second
	defaultSendBuffer   = 64
	maxWSFrameBytes     = 2 << 20
)

var (
	ErrChannelNotFound = errors.New("channel adapter not connected")
	ErrChannelClosed   = errors.New("channel connection closed")
)

type AdapterInfo struct {
	ChannelID    string                `json:"channel_id"`
	Kind         string                `json:"kind"`
	Capabilities []protocol.Capability `json:"capabilities,omitempty"`
}

type Options struct {
	Blobs        BlobStore
	Logger       *slog.Logger
	RPCTimeout   time.Duration
	WriteTimeout time.Duration
	MaxBlobBytes int64
	SendBuffer   int
	OnInbound    func(context.Context, protocol.InboundMessage) error
	OnChatSend   func(context.Context, protocol.ChatSendParams) error
	OnRunTrigger func(context.Context, protocol.RunTriggerParams) error
}

type Hub struct {
	blobs        BlobStore
	logger       *slog.Logger
	rpcTimeout   time.Duration
	writeTimeout time.Duration
	maxBlobBytes int64
	sendBufSize  int
	onInbound    func(context.Context, protocol.InboundMessage) error
	onChatSend   func(context.Context, protocol.ChatSendParams) error
	onRunTrigger func(context.Context, protocol.RunTriggerParams) error

	reqSeq atomic.Uint64

	// mu protects adapters and clients only; never hold mu while locking a clientConn or doing I/O.
	mu       sync.RWMutex
	adapters map[string]*clientConn
	clients  map[*clientConn]struct{}
}

func NewHub(opts Options) *Hub {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	rpcTimeout := opts.RPCTimeout
	if rpcTimeout <= 0 {
		rpcTimeout = defaultRPCTimeout
	}
	writeTimeout := opts.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	maxBlob := opts.MaxBlobBytes
	if maxBlob <= 0 {
		maxBlob = defaultMaxBlobBytes
	}
	sendBuf := opts.SendBuffer
	if sendBuf <= 0 {
		sendBuf = defaultSendBuffer
	}

	return &Hub{
		blobs:        opts.Blobs,
		logger:       logger.With("component", "channel.hub"),
		rpcTimeout:   rpcTimeout,
		writeTimeout: writeTimeout,
		maxBlobBytes: maxBlob,
		sendBufSize:  sendBuf,
		onInbound:    opts.OnInbound,
		onChatSend:   opts.OnChatSend,
		onRunTrigger: opts.OnRunTrigger,
		adapters:     make(map[string]*clientConn),
		clients:      make(map[*clientConn]struct{}),
	}
}

func (h *Hub) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws", h.handleWS)
	mux.HandleFunc("POST /api/blobs", h.handleUploadBlob)
	mux.HandleFunc("GET /api/blobs/{id}", h.handleGetBlob)
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func (h *Hub) Close() {
	h.mu.RLock()
	conns := slices.Collect(maps.Keys(h.clients))
	h.mu.RUnlock()

	for _, c := range conns {
		c.close()
	}
}

func (h *Hub) HasAdapter(channelID string) bool {
	h.mu.RLock()
	_, ok := h.adapters[channelID]
	h.mu.RUnlock()
	return ok
}

func (h *Hub) Capabilities(channelID string) (protocol.CapabilitySet, bool) {
	h.mu.RLock()
	c, ok := h.adapters[channelID]
	h.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return c.capabilitiesCopy(), true
}

func (h *Hub) Adapters() []AdapterInfo {
	h.mu.RLock()
	conns := slices.Collect(maps.Values(h.adapters))
	h.mu.RUnlock()

	out := make([]AdapterInfo, 0, len(conns))
	for _, c := range conns {
		if info, ok := c.adapterInfo(); ok {
			out = append(out, info)
		}
	}
	slices.SortFunc(out, func(a, b AdapterInfo) int {
		return cmp.Compare(a.ChannelID, b.ChannelID)
	})
	return out
}

func (h *Hub) BroadcastEvent(topic, roomID string, payload any) error {
	var rawPayload json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal ui event %q payload: %w", topic, err)
		}
		rawPayload = b
	}

	env, err := protocol.NewNotification(protocol.MethodUIEvent, protocol.UIEventParams{
		Topic:       topic,
		RoomID:      roomID,
		TimestampMs: time.Now().UnixMilli(),
		Payload:     rawPayload,
	})
	if err != nil {
		return fmt.Errorf("build ui event %q: %w", topic, err)
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		return fmt.Errorf("encode ui event %q: %w", topic, err)
	}

	h.mu.RLock()
	conns := slices.Collect(maps.Keys(h.clients))
	h.mu.RUnlock()

	for _, c := range conns {
		if !c.matchesTopic(topic) {
			continue
		}
		if !c.tryEnqueue(frame) {
			h.logger.Warn("dropped ui event for slow subscriber", "topic", topic, "room_id", roomID)
		}
	}
	return nil
}

func (h *Hub) SendOutbound(
	ctx context.Context,
	channelID string,
	roomID string,
	actions []protocol.OutboundAction,
) (protocol.ChannelOutboundResult, error) {
	h.mu.RLock()
	conn, ok := h.adapters[channelID]
	h.mu.RUnlock()
	if !ok {
		return protocol.ChannelOutboundResult{}, fmt.Errorf("channel %q: %w", channelID, ErrChannelNotFound)
	}

	degraded := protocol.DegradeActions(actions, conn.capabilitiesCopy())
	if len(degraded) == 0 {
		return protocol.ChannelOutboundResult{}, nil
	}

	reqID := fmt.Sprintf("hub-%d", h.reqSeq.Add(1))
	env, err := protocol.NewRequest(reqID, protocol.MethodChannelOutbound, protocol.ChannelOutboundParams{
		ChannelID: channelID,
		RoomID:    roomID,
		Actions:   degraded,
	})
	if err != nil {
		return protocol.ChannelOutboundResult{}, fmt.Errorf("build outbound rpc: %w", err)
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		return protocol.ChannelOutboundResult{}, fmt.Errorf("encode outbound rpc: %w", err)
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && h.rpcTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, h.rpcTimeout)
		defer cancel()
	}

	idKey := env.IDKey()
	respCh := conn.registerPending(idKey)
	defer conn.unregisterPending(idKey)

	if err := conn.enqueueCtx(callCtx, frame); err != nil {
		return protocol.ChannelOutboundResult{}, fmt.Errorf("send outbound to %q: %w", channelID, err)
	}

	select {
	case resp := <-respCh:
		var result protocol.ChannelOutboundResult
		if err := resp.DecodeResult(&result); err != nil {
			return protocol.ChannelOutboundResult{}, fmt.Errorf("outbound rpc on %q: %w", channelID, err)
		}
		return result, nil
	case <-callCtx.Done():
		return protocol.ChannelOutboundResult{}, fmt.Errorf("outbound rpc on %q: %w", channelID, callCtx.Err())
	case <-conn.done:
		return protocol.ChannelOutboundResult{}, fmt.Errorf("outbound rpc on %q: %w", channelID, ErrChannelClosed)
	}
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.logger.Debug("websocket accept failed", "err", err)
		return
	}
	ws.SetReadLimit(maxWSFrameBytes)

	conn := newClientConn(ws, h.sendBufSize)

	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()

	connCtx, connCancel := context.WithCancel(r.Context())
	var handlerWG sync.WaitGroup
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		conn.writeLoop(h.writeTimeout, h.logger)
	}()

	defer func() {
		connCancel()
		conn.close()
		removedChannel := h.unregisterConn(conn)
		handlerWG.Wait()
		<-writeDone
		if removedChannel != "" {
			_ = h.BroadcastEvent(protocol.EventChannelStatus, "", map[string]any{
				"channel_id": removedChannel,
				"connected":  false,
			})
		}
	}()

	for {
		msgType, data, err := ws.Read(connCtx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageText {
			continue
		}

		env, err := protocol.DecodeEnvelope(data)
		if err != nil {
			code := protocol.ErrCodeInvalidRequest
			if errors.Is(err, protocol.ErrInvalidJSON) {
				code = protocol.ErrCodeParse
			}
			h.sendError(connCtx, conn, nil, code, err.Error())
			continue
		}

		if env.IsResponse() {
			conn.deliverResponse(env)
			continue
		}
		if env.Method == "" {
			h.sendError(connCtx, conn, env.ID, protocol.ErrCodeInvalidRequest, "missing method")
			continue
		}

		switch env.Method {
		case protocol.MethodChannelRegister:
			h.handleRegister(connCtx, conn, env)
		case protocol.MethodUISubscribe:
			h.handleUISubscribe(connCtx, conn, env)
		case protocol.MethodUIUnsubscribe:
			h.handleUIUnsubscribe(connCtx, conn, env)
		case protocol.MethodChannelInbound, protocol.MethodChatSend, protocol.MethodRunTrigger:
			// Run callbacks off the read loop so handlers can invoke SendOutbound without deadlocking.
			handlerWG.Add(1)
			go func(req protocol.Envelope) {
				defer handlerWG.Done()
				h.dispatchAsync(connCtx, conn, req)
			}(env)
		default:
			if env.IsRequest() {
				h.sendError(connCtx, conn, env.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("unknown method %q", env.Method))
			}
		}
	}
}

func (h *Hub) handleRegister(ctx context.Context, conn *clientConn, env protocol.Envelope) {
	var params protocol.ChannelRegisterParams
	if err := env.DecodeParams(&params); err != nil {
		if env.IsRequest() {
			h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		return
	}
	if params.ChannelID == "" {
		if env.IsRequest() {
			h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, "channel_id is required")
		}
		return
	}

	prevChannel := conn.setAdapter(params.ChannelID, params.Kind, params.Capabilities)

	h.mu.Lock()
	if prevChannel != "" && prevChannel != params.ChannelID && h.adapters[prevChannel] == conn {
		delete(h.adapters, prevChannel)
	}
	superseded := h.adapters[params.ChannelID]
	h.adapters[params.ChannelID] = conn
	h.mu.Unlock()

	if superseded != nil && superseded != conn {
		superseded.close()
	}

	if env.IsRequest() {
		h.sendResult(ctx, conn, env.ID, protocol.ChannelRegisterResult{
			ChannelID: params.ChannelID,
			Accepted:  true,
		})
	}

	_ = h.BroadcastEvent(protocol.EventChannelStatus, "", map[string]any{
		"channel_id":   params.ChannelID,
		"kind":         params.Kind,
		"capabilities": protocol.NewCapabilitySet(params.Capabilities...).Slice(),
		"connected":    true,
	})
}

func (h *Hub) handleUISubscribe(ctx context.Context, conn *clientConn, env protocol.Envelope) {
	var params protocol.UISubscribeParams
	if len(env.Params) > 0 {
		if err := env.DecodeParams(&params); err != nil {
			if env.IsRequest() {
				h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, err.Error())
			}
			return
		}
	}
	topics := conn.subscribe(params.Topics)
	if env.IsRequest() {
		h.sendResult(ctx, conn, env.ID, protocol.UISubscribeResult{Topics: topics})
	}
}

func (h *Hub) handleUIUnsubscribe(ctx context.Context, conn *clientConn, env protocol.Envelope) {
	conn.unsubscribe()
	if env.IsRequest() {
		h.sendResult(ctx, conn, env.ID, protocol.UISubscribeResult{})
	}
}

func (h *Hub) dispatchAsync(ctx context.Context, conn *clientConn, env protocol.Envelope) {
	switch env.Method {
	case protocol.MethodChannelInbound:
		channelID := conn.channelID()
		if channelID == "" {
			if env.IsRequest() {
				h.sendError(ctx, conn, env.ID, protocol.ErrCodeNotRegistered, "adapter must call channel.register first")
			}
			return
		}
		var msg protocol.InboundMessage
		if err := env.DecodeParams(&msg); err != nil {
			if env.IsRequest() {
				h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, err.Error())
			}
			return
		}
		if msg.ChannelID == "" {
			msg.ChannelID = channelID
		} else if msg.ChannelID != channelID {
			if env.IsRequest() {
				h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, "channel_id does not match registered adapter")
			}
			return
		}
		if h.onInbound != nil {
			if err := h.onInbound(ctx, msg); err != nil {
				h.logger.Warn("onInbound failed", "channel_id", msg.ChannelID, "room_id", msg.RoomID, "err", err)
				if env.IsRequest() {
					h.sendError(ctx, conn, env.ID, protocol.ErrCodeInternal, err.Error())
				}
				return
			}
		}
		if env.IsRequest() {
			h.sendResult(ctx, conn, env.ID, protocol.ChannelInboundResult{Accepted: true})
		}

	case protocol.MethodChatSend:
		var params protocol.ChatSendParams
		if err := env.DecodeParams(&params); err != nil {
			if env.IsRequest() {
				h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, err.Error())
			}
			return
		}
		if h.onChatSend != nil {
			if err := h.onChatSend(ctx, params); err != nil {
				if env.IsRequest() {
					h.sendError(ctx, conn, env.ID, protocol.ErrCodeInternal, err.Error())
				}
				return
			}
		}
		if env.IsRequest() {
			h.sendResult(ctx, conn, env.ID, map[string]bool{"accepted": true})
		}

	case protocol.MethodRunTrigger:
		var params protocol.RunTriggerParams
		if err := env.DecodeParams(&params); err != nil {
			if env.IsRequest() {
				h.sendError(ctx, conn, env.ID, protocol.ErrCodeInvalidParams, err.Error())
			}
			return
		}
		if h.onRunTrigger != nil {
			if err := h.onRunTrigger(ctx, params); err != nil {
				if env.IsRequest() {
					h.sendError(ctx, conn, env.ID, protocol.ErrCodeInternal, err.Error())
				}
				return
			}
		}
		if env.IsRequest() {
			h.sendResult(ctx, conn, env.ID, map[string]bool{"accepted": true})
		}
	}
}

func (h *Hub) unregisterConn(conn *clientConn) string {
	chID := conn.channelID()
	var removed string

	h.mu.Lock()
	delete(h.clients, conn)
	if chID != "" && h.adapters[chID] == conn {
		delete(h.adapters, chID)
		removed = chID
	}
	h.mu.Unlock()
	return removed
}

func (h *Hub) sendResult(ctx context.Context, conn *clientConn, id json.RawMessage, result any) {
	env, err := protocol.NewResponse(id, result)
	if err != nil {
		h.logger.Error("build rpc response failed", "err", err)
		return
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		h.logger.Error("encode rpc response failed", "err", err)
		return
	}
	_ = conn.enqueueCtx(ctx, frame)
}

func (h *Hub) sendError(ctx context.Context, conn *clientConn, id json.RawMessage, code int, message string) {
	env, err := protocol.NewErrorResponse(id, code, message, nil)
	if err != nil {
		h.logger.Error("build rpc error failed", "err", err)
		return
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		h.logger.Error("encode rpc error failed", "err", err)
		return
	}
	_ = conn.enqueueCtx(ctx, frame)
}

type clientConn struct {
	ws        *websocket.Conn
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once

	mu         sync.Mutex
	chID       string
	kind       string
	caps       protocol.CapabilitySet
	subscribed bool
	topics     map[string]struct{}
	pending    map[string]chan protocol.Envelope
}

func newClientConn(ws *websocket.Conn, sendBuf int) *clientConn {
	return &clientConn{
		ws:      ws,
		send:    make(chan []byte, sendBuf),
		done:    make(chan struct{}),
		pending: make(map[string]chan protocol.Envelope),
	}
}

// CloseNow avoids blocking up to 5s on a close handshake against an idle or hung peer.
func (c *clientConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.CloseNow()
	})
}

func (c *clientConn) writeLoop(writeTimeout time.Duration, logger *slog.Logger) {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				logger.Debug("ws write failed", "err", err)
				c.close()
				return
			}
		}
	}
}

func (c *clientConn) tryEnqueue(frame []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- frame:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *clientConn) enqueueCtx(ctx context.Context, frame []byte) error {
	select {
	case <-c.done:
		return ErrChannelClosed
	case <-ctx.Done():
		return ctx.Err()
	case c.send <- frame:
		return nil
	}
}

func (c *clientConn) setAdapter(channelID, kind string, caps []protocol.Capability) string {
	c.mu.Lock()
	prev := c.chID
	c.chID = channelID
	c.kind = kind
	c.caps = protocol.NewCapabilitySet(caps...)
	c.mu.Unlock()
	return prev
}

func (c *clientConn) channelID() string {
	c.mu.Lock()
	id := c.chID
	c.mu.Unlock()
	return id
}

func (c *clientConn) capabilitiesCopy() protocol.CapabilitySet {
	c.mu.Lock()
	cp := maps.Clone(c.caps)
	c.mu.Unlock()
	if cp == nil {
		return protocol.NewCapabilitySet()
	}
	return cp
}

func (c *clientConn) adapterInfo() (AdapterInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chID == "" {
		return AdapterInfo{}, false
	}
	return AdapterInfo{
		ChannelID:    c.chID,
		Kind:         c.kind,
		Capabilities: c.caps.Slice(),
	}, true
}

func (c *clientConn) subscribe(topics []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribed = true
	if len(topics) == 0 {
		c.topics = nil
		return nil
	}
	set := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		if t != "" {
			set[t] = struct{}{}
		}
	}
	if len(set) == 0 {
		c.topics = nil
		return nil
	}
	c.topics = set
	return slices.Sorted(maps.Keys(set))
}

func (c *clientConn) unsubscribe() {
	c.mu.Lock()
	c.subscribed = false
	c.topics = nil
	c.mu.Unlock()
}

func (c *clientConn) matchesTopic(topic string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.subscribed {
		return false
	}
	if len(c.topics) == 0 {
		return true
	}
	_, ok := c.topics[topic]
	return ok
}

func (c *clientConn) registerPending(idKey string) chan protocol.Envelope {
	ch := make(chan protocol.Envelope, 1)
	c.mu.Lock()
	c.pending[idKey] = ch
	c.mu.Unlock()
	return ch
}

func (c *clientConn) unregisterPending(idKey string) {
	c.mu.Lock()
	delete(c.pending, idKey)
	c.mu.Unlock()
}

func (c *clientConn) deliverResponse(env protocol.Envelope) {
	c.mu.Lock()
	ch := c.pending[env.IDKey()]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- env:
	default:
	}
}
