package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/protocol"
)

const (
	defaultKernelRPCTimeout = 15 * time.Second
	defaultInitialBackoff   = 500 * time.Millisecond
	defaultMaxBackoff       = 30 * time.Second
	maxKernelFrameBytes     = 2 << 20
)

var napcatCapabilities = []protocol.Capability{
	protocol.CapReply,
	protocol.CapReact,
	protocol.CapPoke,
	protocol.CapVoice,
	protocol.CapSticker,
	protocol.CapMultiBubble,
}

var ErrKernelClosed = errors.New("kernel websocket closed")

type BridgeConfig struct {
	ChannelID       string
	KernelHTTPURL   string
	KernelWSURL     string
	NapCatWSURL     string
	NapCatListen    string
	NapCatToken     string
	FFmpegPath      string
	MaxForwardDepth int
	ActionTimeout   time.Duration
	RPCTimeout      time.Duration
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	HTTPClient      *http.Client
	AudioTranscoder AudioTranscodeFunc
	Logger          *slog.Logger
}

type Bridge struct {
	cfg        BridgeConfig
	kernelWS   string
	blobs      *HTTPBlobClient
	httpClient *http.Client
	logger     *slog.Logger

	// stateMu protects only pointer swaps and readiness broadcast; never held across WS I/O.
	stateMu     sync.Mutex
	onebot      *OneBotClient
	kernel      *kernelConn
	reverseAddr string
	stateCh     chan struct{}
}

func NewBridge(cfg BridgeConfig) (*Bridge, error) {
	if cfg.ChannelID == "" {
		cfg.ChannelID = defaultChannelID
	}
	if cfg.KernelHTTPURL == "" {
		return nil, errors.New("kernel HTTP URL is required")
	}
	if cfg.NapCatWSURL == "" && cfg.NapCatListen == "" {
		return nil, errors.New("either NapCat WS URL or NapCat reverse listen address is required")
	}
	if cfg.NapCatWSURL != "" && cfg.NapCatListen != "" {
		return nil, errors.New("configure either NapCat WS URL or NapCat reverse listen address, not both")
	}

	kernelWS := cfg.KernelWSURL
	if kernelWS == "" {
		var err error
		kernelWS, err = deriveKernelWSURL(cfg.KernelHTTPURL)
		if err != nil {
			return nil, err
		}
	}
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = defaultActionTimeout
	}
	if cfg.RPCTimeout <= 0 {
		cfg.RPCTimeout = defaultKernelRPCTimeout
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = defaultInitialBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	if cfg.MaxForwardDepth <= 0 {
		cfg.MaxForwardDepth = defaultMaxForwardDepth
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Bridge{
		cfg:        cfg,
		kernelWS:   kernelWS,
		blobs:      NewHTTPBlobClient(cfg.KernelHTTPURL, httpClient),
		httpClient: httpClient,
		logger:     logger.With("component", "napcat.bridge", "channel_id", cfg.ChannelID),
		stateCh:    make(chan struct{}),
	}, nil
}

func (b *Bridge) Run(ctx context.Context) error {
	var (
		wg          sync.WaitGroup
		errCh       = make(chan error, 1)
		runCtx, end = context.WithCancel(ctx)
	)
	defer end()

	wg.Add(2)
	go func() {
		defer wg.Done()
		b.runKernelLoop(runCtx)
	}()
	go func() {
		defer wg.Done()
		if b.cfg.NapCatListen != "" {
			if err := b.runNapCatReverseServer(runCtx); err != nil {
				select {
				case errCh <- err:
				default:
				}
				end()
			}
			return
		}
		b.runNapCatClientLoop(runCtx)
	}()

	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (b *Bridge) WaitReady(ctx context.Context) error {
	for {
		b.stateMu.Lock()
		ready := b.onebot != nil && b.kernel != nil
		ch := b.stateCh
		b.stateMu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (b *Bridge) ReverseAddr(ctx context.Context) (string, error) {
	for {
		b.stateMu.Lock()
		addr := b.reverseAddr
		ch := b.stateCh
		b.stateMu.Unlock()
		if addr != "" {
			return addr, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ch:
		}
	}
}

func (b *Bridge) broadcastStateLocked() {
	close(b.stateCh)
	b.stateCh = make(chan struct{})
}

func (b *Bridge) setOneBot(c *OneBotClient) *OneBotClient {
	b.stateMu.Lock()
	prev := b.onebot
	b.onebot = c
	b.broadcastStateLocked()
	b.stateMu.Unlock()
	return prev
}

func (b *Bridge) clearOneBot(c *OneBotClient) {
	b.stateMu.Lock()
	if b.onebot == c {
		b.onebot = nil
		b.broadcastStateLocked()
	}
	b.stateMu.Unlock()
}

func (b *Bridge) activeOneBot() *OneBotClient {
	b.stateMu.Lock()
	c := b.onebot
	b.stateMu.Unlock()
	return c
}

func (b *Bridge) setKernel(c *kernelConn) {
	b.stateMu.Lock()
	b.kernel = c
	b.broadcastStateLocked()
	b.stateMu.Unlock()
}

func (b *Bridge) clearKernel(c *kernelConn) {
	b.stateMu.Lock()
	if b.kernel == c {
		b.kernel = nil
		b.broadcastStateLocked()
	}
	b.stateMu.Unlock()
}

func (b *Bridge) activeKernel() *kernelConn {
	b.stateMu.Lock()
	c := b.kernel
	b.stateMu.Unlock()
	return c
}

func (b *Bridge) runKernelLoop(ctx context.Context) {
	backoff := b.cfg.InitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		connected, err := b.serveKernelOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = b.cfg.InitialBackoff
		}
		if err != nil {
			b.logger.Warn("kernel ws disconnected; scheduling reconnect", "backoff", backoff, "err", err)
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, b.cfg.MaxBackoff)
	}
}

func (b *Bridge) serveKernelOnce(ctx context.Context) (bool, error) {
	dialCtx, cancel := context.WithTimeout(ctx, b.cfg.RPCTimeout)
	ws, _, err := websocket.Dial(dialCtx, b.kernelWS, nil)
	cancel()
	if err != nil {
		return false, fmt.Errorf("dial kernel ws: %w", err)
	}

	kc := newKernelConn(ctx, ws, b.cfg.RPCTimeout, b.logger, b.handleKernelOutbound)
	defer func() {
		b.clearKernel(kc)
		kc.Close()
		kc.Wait()
	}()

	var regRes protocol.ChannelRegisterResult
	err = kc.Call(ctx, protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID:    b.cfg.ChannelID,
		Kind:         "napcat",
		Capabilities: napcatCapabilities,
	}, &regRes)
	if err != nil {
		return false, fmt.Errorf("channel.register failed: %w", err)
	}
	if !regRes.Accepted {
		return false, errors.New("channel.register was rejected by kernel")
	}

	b.setKernel(kc)
	b.logger.Info("connected and registered with kernel", "ws_url", b.kernelWS)

	select {
	case <-ctx.Done():
		return true, nil
	case <-kc.Done():
		return true, ErrKernelClosed
	}
}

func (b *Bridge) runNapCatClientLoop(ctx context.Context) {
	backoff := b.cfg.InitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		connected, err := b.serveNapCatClientOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = b.cfg.InitialBackoff
		}
		if err != nil {
			b.logger.Warn("napcat ws disconnected; scheduling reconnect", "backoff", backoff, "err", err)
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, b.cfg.MaxBackoff)
	}
}

func (b *Bridge) serveNapCatClientOnce(ctx context.Context) (bool, error) {
	var dialOpts *websocket.DialOptions
	if b.cfg.NapCatToken != "" {
		dialOpts = &websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + b.cfg.NapCatToken},
			},
		}
	}

	dialCtx, cancel := context.WithTimeout(ctx, b.cfg.ActionTimeout)
	ws, _, err := websocket.Dial(dialCtx, b.cfg.NapCatWSURL, dialOpts)
	cancel()
	if err != nil {
		return false, fmt.Errorf("dial napcat ws: %w", err)
	}

	client := NewOneBotClient(ctx, ws, OneBotClientOptions{
		ActionTimeout: b.cfg.ActionTimeout,
		Logger:        b.logger,
		OnEvent:       b.handleOneBotEvent,
	})
	if prev := b.setOneBot(client); prev != nil {
		prev.Close()
	}
	b.logger.Info("connected to napcat ws", "ws_url", b.cfg.NapCatWSURL)

	defer func() {
		b.clearOneBot(client)
		client.Close()
		client.Wait()
	}()

	select {
	case <-ctx.Done():
		return true, nil
	case <-client.Done():
		return true, ErrOneBotClosed
	}
}

func (b *Bridge) runNapCatReverseServer(ctx context.Context) error {
	ln, err := net.Listen("tcp", b.cfg.NapCatListen)
	if err != nil {
		return fmt.Errorf("listen napcat reverse ws %q: %w", b.cfg.NapCatListen, err)
	}

	b.stateMu.Lock()
	b.reverseAddr = ln.Addr().String()
	b.broadcastStateLocked()
	b.stateMu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !b.authorizeReverseWS(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			b.logger.Warn("accept napcat reverse ws failed", "err", err)
			return
		}

		client := NewOneBotClient(ctx, ws, OneBotClientOptions{
			ActionTimeout: b.cfg.ActionTimeout,
			Logger:        b.logger,
			OnEvent:       b.handleOneBotEvent,
		})
		if prev := b.setOneBot(client); prev != nil {
			prev.Close()
		}
		b.logger.Info("accepted napcat reverse ws connection", "remote", r.RemoteAddr)

		defer func() {
			b.clearOneBot(client)
			client.Close()
			client.Wait()
		}()

		select {
		case <-ctx.Done():
		case <-client.Done():
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	b.logger.Info("listening for napcat reverse ws", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve napcat reverse ws: %w", err)
	}
	return nil
}

func (b *Bridge) authorizeReverseWS(r *http.Request) bool {
	if b.cfg.NapCatToken == "" {
		return true
	}
	want := []byte(b.cfg.NapCatToken)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		got := []byte(strings.TrimPrefix(auth, "Bearer "))
		if subtle.ConstantTimeCompare(got, want) == 1 {
			return true
		}
	}
	if q := r.URL.Query().Get("access_token"); q != "" {
		if subtle.ConstantTimeCompare([]byte(q), want) == 1 {
			return true
		}
	}
	return false
}

func (b *Bridge) handleOneBotEvent(ctx context.Context, ob *OneBotClient, ev OneBotEvent) {
	norm := NewNormalizer(NormalizerOptions{
		ChannelID:       b.cfg.ChannelID,
		Blobs:           b.blobs,
		Forwards:        ob,
		Media:           ob,
		HTTPClient:      b.httpClient,
		AudioTranscoder: b.cfg.AudioTranscoder,
		FFmpegPath:      b.cfg.FFmpegPath,
		MaxForwardDepth: b.cfg.MaxForwardDepth,
		Logger:          b.logger,
	})

	msg, ok, err := norm.NormalizeGroupMessage(ctx, ev)
	if err != nil {
		b.logger.Warn("normalize group message failed", "group_id", ev.GroupID, "err", err)
		return
	}
	if !ok {
		return
	}

	kc := b.activeKernel()
	if kc == nil {
		b.logger.Warn("kernel ws not connected; dropping inbound message", "room_id", msg.RoomID, "msg_id", msg.MsgID)
		return
	}

	var ack protocol.ChannelInboundResult
	if err := kc.Call(ctx, protocol.MethodChannelInbound, msg, &ack); err != nil {
		b.logger.Warn("forward inbound message to kernel failed", "room_id", msg.RoomID, "msg_id", msg.MsgID, "err", err)
	}
}

func (b *Bridge) handleKernelOutbound(ctx context.Context, env protocol.Envelope) (any, int, error) {
	var params protocol.ChannelOutboundParams
	if err := env.DecodeParams(&params); err != nil {
		return nil, protocol.ErrCodeInvalidParams, err
	}

	ob := b.activeOneBot()
	if ob == nil {
		return nil, protocol.ErrCodeDeliveryFailed, errors.New("napcat websocket is not connected")
	}

	exec := NewActionExecutor(ob, b.blobs, b.logger)
	res, err := exec.ExecuteOutbound(ctx, params.RoomID, params.Actions)
	if err != nil {
		return nil, protocol.ErrCodeDeliveryFailed, err
	}
	return res, 0, nil
}

type outboundHandlerFunc func(ctx context.Context, env protocol.Envelope) (result any, errCode int, err error)

type kernelConn struct {
	ws           *websocket.Conn
	rpcTimeout   time.Duration
	writeTimeout time.Duration
	logger       *slog.Logger
	onOutbound   outboundHandlerFunc

	seq       atomic.Uint64
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// mu protects pending only; never hold mu across WS I/O.
	mu      sync.Mutex
	pending map[string]chan protocol.Envelope
}

func newKernelConn(
	ctx context.Context,
	ws *websocket.Conn,
	rpcTimeout time.Duration,
	logger *slog.Logger,
	onOutbound outboundHandlerFunc,
) *kernelConn {
	ws.SetReadLimit(maxKernelFrameBytes)
	runCtx, cancel := context.WithCancel(ctx)
	kc := &kernelConn{
		ws:           ws,
		rpcTimeout:   rpcTimeout,
		writeTimeout: defaultWriteTimeout,
		logger:       logger.With("component", "napcat.kernel_ws"),
		onOutbound:   onOutbound,
		send:         make(chan []byte, defaultSendBuffer),
		done:         make(chan struct{}),
		cancel:       cancel,
		pending:      make(map[string]chan protocol.Envelope),
	}

	kc.wg.Add(2)
	go func() {
		defer kc.wg.Done()
		kc.writeLoop()
	}()
	go func() {
		defer kc.wg.Done()
		kc.readLoop(runCtx)
	}()
	return kc
}

func (kc *kernelConn) Done() <-chan struct{} {
	return kc.done
}

func (kc *kernelConn) Close() {
	kc.closeOnce.Do(func() {
		kc.cancel()
		close(kc.done)
		_ = kc.ws.CloseNow()
	})
}

func (kc *kernelConn) Wait() {
	kc.wg.Wait()
}

func (kc *kernelConn) writeLoop() {
	for {
		select {
		case <-kc.done:
			return
		case frame := <-kc.send:
			wctx, cancel := context.WithTimeout(context.Background(), kc.writeTimeout)
			err := kc.ws.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				kc.logger.Debug("kernel ws write failed", "err", err)
				kc.Close()
				return
			}
		}
	}
}

func (kc *kernelConn) readLoop(ctx context.Context) {
	defer kc.Close()

	for {
		msgType, data, err := kc.ws.Read(ctx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageText {
			continue
		}

		env, err := protocol.DecodeEnvelope(data)
		if err != nil {
			kc.logger.Warn("decode kernel jsonrpc frame failed", "err", err)
			continue
		}

		if env.IsResponse() {
			kc.deliverResponse(env)
			continue
		}

		if env.Method == protocol.MethodChannelOutbound && kc.onOutbound != nil {
			// Run outbound execution off the read loop so concurrent inbound RPC acks are not blocked.
			kc.wg.Add(1)
			go func(req protocol.Envelope) {
				defer kc.wg.Done()
				res, errCode, err := kc.onOutbound(ctx, req)
				if !req.IsRequest() {
					return
				}
				if err != nil {
					if errCode == 0 {
						errCode = protocol.ErrCodeDeliveryFailed
					}
					kc.sendError(ctx, req.ID, errCode, err.Error())
					return
				}
				kc.sendResult(ctx, req.ID, res)
			}(env)
			continue
		}

		if env.IsRequest() {
			kc.sendError(ctx, env.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("unknown method %q", env.Method))
		}
	}
}

func (kc *kernelConn) Call(ctx context.Context, method string, params any, dst any) error {
	reqID := fmt.Sprintf("nc-%d", kc.seq.Add(1))
	env, err := protocol.NewRequest(reqID, method, params)
	if err != nil {
		return fmt.Errorf("build kernel rpc %q: %w", method, err)
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		return fmt.Errorf("encode kernel rpc %q: %w", method, err)
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && kc.rpcTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, kc.rpcTimeout)
		defer cancel()
	}

	idKey := env.IDKey()
	respCh := kc.registerPending(idKey)
	defer kc.unregisterPending(idKey)

	select {
	case <-kc.done:
		return fmt.Errorf("kernel rpc %q: %w", method, ErrKernelClosed)
	case <-callCtx.Done():
		return fmt.Errorf("kernel rpc %q: %w", method, callCtx.Err())
	case kc.send <- frame:
	}

	select {
	case resp := <-respCh:
		if dst == nil {
			if resp.Error != nil {
				return resp.Error
			}
			return nil
		}
		if err := resp.DecodeResult(dst); err != nil {
			return fmt.Errorf("kernel rpc %q: %w", method, err)
		}
		return nil
	case <-callCtx.Done():
		return fmt.Errorf("kernel rpc %q: %w", method, callCtx.Err())
	case <-kc.done:
		return fmt.Errorf("kernel rpc %q: %w", method, ErrKernelClosed)
	}
}

func (kc *kernelConn) registerPending(idKey string) chan protocol.Envelope {
	ch := make(chan protocol.Envelope, 1)
	kc.mu.Lock()
	kc.pending[idKey] = ch
	kc.mu.Unlock()
	return ch
}

func (kc *kernelConn) unregisterPending(idKey string) {
	kc.mu.Lock()
	delete(kc.pending, idKey)
	kc.mu.Unlock()
}

func (kc *kernelConn) deliverResponse(env protocol.Envelope) {
	kc.mu.Lock()
	ch := kc.pending[env.IDKey()]
	kc.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- env:
	default:
	}
}

func (kc *kernelConn) sendResult(ctx context.Context, id json.RawMessage, result any) {
	env, err := protocol.NewResponse(id, result)
	if err != nil {
		kc.logger.Error("build kernel rpc response failed", "err", err)
		return
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		kc.logger.Error("encode kernel rpc response failed", "err", err)
		return
	}
	select {
	case <-kc.done:
	case <-ctx.Done():
	case kc.send <- frame:
	}
}

func (kc *kernelConn) sendError(ctx context.Context, id json.RawMessage, code int, msg string) {
	env, err := protocol.NewErrorResponse(id, code, msg, nil)
	if err != nil {
		kc.logger.Error("build kernel rpc error failed", "err", err)
		return
	}
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		kc.logger.Error("encode kernel rpc error failed", "err", err)
		return
	}
	select {
	case <-kc.done:
	case <-ctx.Done():
	case kc.send <- frame:
	}
}

func deriveKernelWSURL(httpURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(httpURL))
	if err != nil {
		return "", fmt.Errorf("invalid kernel HTTP URL %q: %w", httpURL, err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported kernel URL scheme %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
