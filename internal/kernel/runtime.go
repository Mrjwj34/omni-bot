package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/fishaudio"
	"omni-bot/internal/ai/google"
	"omni-bot/internal/ai/openaiembed"
	"omni-bot/internal/ai/whisper"
	"omni-bot/internal/channel"
	"omni-bot/internal/meme"
	"omni-bot/internal/memory"
	"omni-bot/internal/persona"
	"omni-bot/internal/protocol"
	"omni-bot/internal/store"
	"omni-bot/internal/tools"
	"omni-bot/internal/tools/mcp"
	"omni-bot/internal/tools/sandbox"
)

const (
	DefaultDebugRoomID    = "webui-debug"
	DefaultDebugChannelID = "webui"
)

// RuntimeOptions configures a complete Bot Kernel Runtime.
type RuntimeOptions struct {
	DataDir        string
	Store          *store.Store
	Blobs          *store.BlobStore
	SandboxDir     string
	BotName        string
	BotUserID      string
	GateThreshold  float64
	DebounceWindow time.Duration
	MaxSteps       int
	Location       *time.Location
	Clock          func() time.Time
	Logger         *slog.Logger
	InitialAI      *ai.Registry
	DisableAsyncBg bool
}

// Runtime bundles all Kernel subsystems, hot-reloadable AI/Tool/MCP state, Channel Hub, and HTTP API.
type Runtime struct {
	Store    *store.Store
	Blobs    *store.BlobStore
	Hub      *channel.Hub
	Memory   *memory.Engine
	Emotion  *persona.EmotionEngine
	Persona  *persona.PersonaEngine
	Memes    *meme.Vault
	MCP      *mcp.Manager
	Pipeline *Pipeline

	ownsStore  bool
	sandboxDir string
	location   *time.Location
	clock      func() time.Time
	logger     *slog.Logger
	startedAt  time.Time

	aiReg        atomic.Pointer[ai.Registry]
	features     atomic.Pointer[store.FeatureFlags]
	webSearchCfg atomic.Pointer[tools.WebSearchConfig]

	// reloadMu serializes config hot-reload without blocking in-flight Runs reading atomic pointers.
	reloadMu  sync.Mutex
	mcpErrsMu sync.RWMutex
	mcpErrs   map[string]string

	dreamMu     sync.Mutex
	dreamStatus APIDreamingStatus

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup
}

// NewRuntime initializes the Bot Kernel runtime, hydrating storage, memory vector cache,
// persona/emotion engines, meme vault, MCP manager, Channel Hub, and 3-stage Pipeline.
func NewRuntime(ctx context.Context, opts RuntimeOptions) (*Runtime, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	loc := opts.Location
	if loc == nil {
		loc = time.Local
	}

	st := opts.Store
	ownsStore := false
	if st == nil {
		if strings.TrimSpace(opts.DataDir) == "" {
			return nil, errors.New("kernel: either Store or DataDir is required")
		}
		var err error
		st, err = store.Open(ctx, filepath.Join(opts.DataDir, "omni.db"))
		if err != nil {
			return nil, fmt.Errorf("kernel: open store: %w", err)
		}
		ownsStore = true
	}

	blobs := opts.Blobs
	if blobs == nil {
		if strings.TrimSpace(opts.DataDir) == "" {
			if ownsStore {
				_ = st.Close()
			}
			return nil, errors.New("kernel: either Blobs or DataDir is required")
		}
		var err error
		blobs, err = store.NewBlobStore(opts.DataDir)
		if err != nil {
			if ownsStore {
				_ = st.Close()
			}
			return nil, fmt.Errorf("kernel: init blob store: %w", err)
		}
	}

	sandboxDir := strings.TrimSpace(opts.SandboxDir)
	if sandboxDir == "" && strings.TrimSpace(opts.DataDir) != "" {
		sandboxDir = filepath.Join(opts.DataDir, "sandbox")
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())

	rt := &Runtime{
		Store:      st,
		Blobs:      blobs,
		ownsStore:  ownsStore,
		sandboxDir: sandboxDir,
		location:   loc,
		clock:      clock,
		logger:     logger.With("component", "kernel.runtime"),
		startedAt:  clock(),
		mcpErrs:    make(map[string]string),
		bgCtx:      bgCtx,
		bgCancel:   bgCancel,
	}

	defFlags := store.DefaultFeatureFlags()
	rt.features.Store(&defFlags)

	if opts.InitialAI != nil {
		rt.aiReg.Store(opts.InitialAI)
	} else {
		rt.aiReg.Store(ai.NewRegistry())
	}

	// Ensure default persona core & traits exist on first boot without overwriting existing data.
	if err := rt.ensureDefaultPersona(ctx); err != nil {
		rt.cleanupOnInitError()
		return nil, err
	}

	rt.Emotion = persona.NewEmotionEngine(st, persona.WithClock(clock), persona.WithLogger(logger))
	rt.Persona = persona.NewPersonaEngine(st, persona.WithPersonaClock(clock), persona.WithPersonaLogger(logger), persona.WithEmotionEngine(rt.Emotion))

	dynEmbed := &dynamicEmbedder{getAI: rt.AIRegistry}
	memEng, err := memory.NewEngine(ctx, st, dynEmbed, memory.Config{
		Location: loc,
		Clock:    clock,
		Logger:   logger,
	})
	if err != nil {
		rt.cleanupOnInitError()
		return nil, fmt.Errorf("kernel: init memory engine: %w", err)
	}
	rt.Memory = memEng

	vault, err := meme.NewVault(meme.Options{
		Store:    st,
		Blobs:    blobs,
		Embedder: &dynamicMemeEmbedder{getAI: rt.AIRegistry},
		Logger:   logger,
	})
	if err != nil {
		rt.cleanupOnInitError()
		return nil, fmt.Errorf("kernel: init meme vault: %w", err)
	}
	rt.Memes = vault

	rt.MCP = mcp.NewManager(mcp.ManagerOptions{
		SecretResolver: st,
		Logger:         logger,
	})

	rt.Hub = channel.NewHub(channel.Options{
		Blobs:        blobAdapter{blobs: blobs},
		Logger:       logger,
		OnInbound:    rt.HandleInbound,
		OnChatSend:   rt.HandleChatSend,
		OnRunTrigger: rt.HandleRunTrigger,
	})

	pipe, err := NewPipeline(PipelineConfig{
		Store:          st,
		Blobs:          blobs,
		Hub:            rt.Hub,
		AIGetter:       rt.AIRegistry,
		Memory:         rt.Memory,
		Emotion:        rt.Emotion,
		Persona:        rt.Persona,
		Memes:          rt.Memes,
		ToolsProvider:  rt.BuildRunTools,
		FeatureFlags:   rt.FeatureFlags,
		BotName:        opts.BotName,
		BotUserID:      opts.BotUserID,
		GateThreshold:  opts.GateThreshold,
		DebounceWindow: opts.DebounceWindow,
		MaxSteps:       opts.MaxSteps,
		Location:       loc,
		Clock:          clock,
		Logger:         logger,
		DisableAsyncBg: opts.DisableAsyncBg,
	})
	if err != nil {
		rt.cleanupOnInitError()
		return nil, fmt.Errorf("kernel: init pipeline: %w", err)
	}
	rt.Pipeline = pipe

	// Load dynamic runtime config from SQLite (unless InitialAI was explicitly provided for unit tests).
	if opts.InitialAI == nil {
		if err := rt.Reload(ctx); err != nil {
			rt.logger.Warn("initial runtime config reload reported warnings", "err", err)
		}
	} else {
		if cfg, err := st.LoadRuntimeConfig(ctx); err == nil {
			f := cfg.Features
			rt.features.Store(&f)
		}
	}

	return rt, nil
}

func (rt *Runtime) cleanupOnInitError() {
	rt.bgCancel()
	if rt.Hub != nil {
		rt.Hub.Close()
	}
	if rt.MCP != nil {
		_ = rt.MCP.Close()
	}
	if rt.ownsStore && rt.Store != nil {
		_ = rt.Store.Close()
	}
}

func (rt *Runtime) ensureDefaultPersona(ctx context.Context) error {
	if _, ok, err := rt.Store.GetPersonaCore(ctx, store.DefaultPersonaRoomID); err != nil {
		return fmt.Errorf("kernel: check persona core: %w", err)
	} else if !ok {
		core := store.PersonaCore{
			RoomID:      store.DefaultPersonaRoomID,
			Beliefs:     persona.DefaultBeliefs(),
			Temperament: persona.TemperamentToStore(persona.TemperamentFromStore(store.TemperamentParams{})),
			UpdatedAtMs: rt.clock().UnixMilli(),
		}
		if err := rt.Store.SetPersonaCore(ctx, core, ""); err != nil {
			return fmt.Errorf("kernel: seed persona core: %w", err)
		}
	}
	traits, err := rt.Store.ListPersonaTraits(ctx)
	if err != nil {
		return fmt.Errorf("kernel: check persona traits: %w", err)
	}
	if len(traits) == 0 {
		if _, err := rt.Store.ReplacePersonaTraits(ctx, store.DefaultPersonaRoomID, persona.DefaultTraits(), "initial_seed"); err != nil {
			return fmt.Errorf("kernel: seed persona traits: %w", err)
		}
	}
	return nil
}

// AIRegistry returns the current atomically swapped AI Registry snapshot.
func (rt *Runtime) AIRegistry() *ai.Registry {
	return rt.aiReg.Load()
}

// FeatureFlags returns the current hot-reloaded feature flag snapshot.
func (rt *Runtime) FeatureFlags() store.FeatureFlags {
	if p := rt.features.Load(); p != nil {
		return *p
	}
	return store.DefaultFeatureFlags()
}

// Reload loads dynamic config from SQLite and atomically rebuilds the AI Registry,
// feature flags (including Bash sandbox toggle), web search config, and MCP connections
// without interrupting any in-flight Runs.
func (rt *Runtime) Reload(ctx context.Context) error {
	rt.reloadMu.Lock()
	defer rt.reloadMu.Unlock()

	cfg, err := rt.Store.LoadRuntimeConfig(ctx)
	if err != nil {
		return fmt.Errorf("kernel: load runtime config: %w", err)
	}

	flags := cfg.Features
	rt.features.Store(&flags)

	newReg, searchCfg, regErr := rt.buildAIRegistry(ctx, cfg)
	rt.aiReg.Store(newReg)
	rt.webSearchCfg.Store(searchCfg)

	var mcpErr error
	if rt.MCP != nil {
		mcpErr = rt.MCP.Reload(ctx, cfg.MCPServers)
		rt.recordMCPErrors(cfg.MCPServers, mcpErr)
	}

	if rt.Hub != nil {
		_ = rt.Hub.BroadcastEvent(protocol.EventConfigUpdated, "", map[string]any{
			"updated_at": rt.clock().UTC().Format(time.RFC3339),
		})
	}

	return errors.Join(regErr, mcpErr)
}

func (rt *Runtime) recordMCPErrors(servers []store.MCPServerConfig, err error) {
	rt.mcpErrsMu.Lock()
	defer rt.mcpErrsMu.Unlock()
	clear(rt.mcpErrs)
	if err == nil {
		return
	}
	msg := err.Error()
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		label := s.Name
		if label == "" {
			label = s.ID
		}
		if label != "" && strings.Contains(msg, label) {
			rt.mcpErrs[s.ID] = msg
		}
	}
}

func (rt *Runtime) buildAIRegistry(ctx context.Context, cfg store.RuntimeConfig) (*ai.Registry, *tools.WebSearchConfig, error) {
	reg := ai.NewRegistry()
	bAdapter := blobAdapter{blobs: rt.Blobs}

	google.RegisterFactories(reg, google.WithBlobReader(bAdapter))
	openaiembed.RegisterFactory(reg)
	fishaudio.RegisterFactory(reg)
	whisper.RegisterFactory(reg, whisper.WithBlobReader(bAdapter))

	// Wrap LanguageFactory with MediaFallbackMiddleware so non-multimodal models transparently degrade media.
	reg.RegisterLanguageFactory(google.ProtocolInteractions, func(rm ai.ResolvedModel) (ai.LanguageModel, error) {
		inner, err := google.NewLanguageModel(context.Background(), rm, google.WithBlobReader(bAdapter))
		if err != nil {
			return nil, err
		}
		var asr ai.TranscriptionModel
		if tm, err := reg.Transcription(ai.SlotASR); err == nil {
			asr = tm
		}
		return ai.NewMediaFallbackMiddleware(inner, ai.FallbackConfig{
			Transcriber: asr,
			BlobReader:  bAdapter,
		}), nil
	})

	var errs []error
	var searchCfg *tools.WebSearchConfig

	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		var apiKeys []string
		secretKey := strings.TrimSpace(p.SecretKey)
		if secretKey == "" {
			secretKey = providerSecretKey(p.ID)
		}
		if raw, ok, err := rt.Store.ResolveSecret(ctx, secretKey); err == nil && ok {
			apiKeys = splitAPIKeys(raw)
		}

		if strings.EqualFold(p.Kind, "deepseek") || strings.EqualFold(p.ID, "deepseek") {
			var key string
			if len(apiKeys) > 0 {
				key = apiKeys[0]
			}
			searchCfg = &tools.WebSearchConfig{
				BaseURL: p.BaseURL,
				APIKey:  key,
			}
		}

		if err := reg.RegisterProvider(ai.ProviderConfig{
			ID:       p.ID,
			Kind:     p.Kind,
			BaseURL:  p.BaseURL,
			APIKeys:  apiKeys,
			Headers:  p.Headers,
			ProxyURL: p.ProxyURL,
		}); err != nil {
			errs = append(errs, err)
		}
	}

	for _, m := range cfg.Models {
		if err := reg.RegisterModel(ai.ModelConfig{
			ID:         m.ID,
			ProviderID: m.ProviderID,
			Protocol:   m.Protocol,
			ModelID:    m.ModelID,
			Capabilities: ai.Capabilities{
				Vision:  m.Capabilities.Vision,
				AudioIn: m.Capabilities.AudioIn,
				VideoIn: m.Capabilities.VideoIn,
				ToolUse: m.Capabilities.ToolCalls,
			},
			DefaultParams: m.DefaultParams,
		}); err != nil {
			errs = append(errs, err)
		}
	}

	bind := func(slot ai.Slot, modelID string) {
		if id := strings.TrimSpace(modelID); id != "" {
			if err := reg.BindSlot(slot, id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	bind(ai.SlotChat, cfg.Slots.Chat)
	bind(ai.SlotDecision, cfg.Slots.Decision)
	bind(ai.SlotSummary, cfg.Slots.Summary)
	bind(ai.SlotVision, cfg.Slots.Vision)
	bind(ai.SlotEmbedding, cfg.Slots.Embedding)
	bind(ai.SlotTTS, cfg.Slots.TTS)
	bind(ai.SlotASR, cfg.Slots.STT)

	return reg, searchCfg, errors.Join(errs...)
}

// BuildRunTools creates an isolated frozen tools.Registry snapshot for one Run,
// combining built-in tools, conditional default-OFF sandbox tools, and pinned MCP tools.
func (rt *Runtime) BuildRunTools(roomID string) (*tools.Registry, func()) {
	reg := tools.NewRegistry()
	flags := rt.FeatureFlags()

	if rt.Memory != nil && flags.MemoryEnabled {
		_ = reg.Register(tools.NewMemoryRecallTool(&runtimeMemoryRecaller{
			engine: rt.Memory,
			roomID: roomID,
			loc:    rt.location,
		}))
	}
	_ = reg.Register(tools.NewWebFetchTool(tools.WebFetchConfig{}))
	if sc := rt.webSearchCfg.Load(); sc != nil {
		_ = reg.Register(tools.NewWebSearchTool(*sc))
	}
	if rt.Memes != nil {
		_ = reg.Register(tools.NewManageMemeTool(rt.Memes))
	}

	// Default-OFF Bash/file sandbox toolset: only registered when explicitly enabled in RuntimeConfig.
	if flags.BashToolsEnabled && rt.sandboxDir != "" {
		if _, err := sandbox.RegisterIfEnabled(reg, sandbox.Config{
			Enabled:    true,
			SandboxDir: rt.sandboxDir,
		}); err != nil {
			rt.logger.Warn("failed to register sandbox tools", "err", err)
		}
	}

	release := func() {}
	if rt.MCP != nil {
		snap, rel := rt.MCP.AcquireSnapshot()
		release = rel
		if err := snap.RegisterInto(reg); err != nil {
			rt.logger.Warn("register mcp tools into run snapshot", "err", err)
		}
	}

	return reg, release
}

// HandleInbound routes an adapter inbound message through the Kernel Pipeline.
func (rt *Runtime) HandleInbound(ctx context.Context, msg protocol.InboundMessage) error {
	return rt.Pipeline.HandleInbound(ctx, msg)
}

// HandleChatSend handles a WebUI debug console message, persisting it and dispatching a BypassGate Run.
func (rt *Runtime) HandleChatSend(ctx context.Context, params protocol.ChatSendParams) error {
	roomID := strings.TrimSpace(params.RoomID)
	if roomID == "" {
		roomID = DefaultDebugRoomID
	}
	channelID := strings.TrimSpace(params.ChannelID)
	if channelID == "" {
		channelID = DefaultDebugChannelID
	}
	text := strings.TrimSpace(params.Text)
	if text == "" && len(params.Media) == 0 {
		return errors.New("chat message text or media is required")
	}

	var blocks []protocol.ContentBlock
	if text != "" {
		blocks = append(blocks, protocol.ContentBlock{
			Type: protocol.BlockText,
			Text: text,
		})
	}
	for _, m := range params.Media {
		ref := m
		bType := protocol.BlockImage
		switch {
		case strings.HasPrefix(m.MIMEType, "audio/"):
			bType = protocol.BlockAudio
		case strings.HasPrefix(m.MIMEType, "video/"):
			bType = protocol.BlockVideo
		}
		blocks = append(blocks, protocol.ContentBlock{
			Type:  bType,
			Media: &ref,
		})
	}

	nowMs := rt.clock().UnixMilli()
	msg := protocol.InboundMessage{
		ChannelID:    channelID,
		RoomID:       roomID,
		MsgID:        "ui-" + randomHex(6),
		SenderID:     "webui-user",
		SenderName:   "调试者",
		IsBot:        false,
		IsAtBot:      true,
		TimestampMs:  nowMs,
		Blocks:       blocks,
		ReplyToMsgID: params.ReplyToMsgID,
	}

	blocksJSON, _ := json.Marshal(msg.Blocks)
	if _, err := rt.Store.AppendTimelineMessage(ctx, store.TimelineMessage{
		ChannelID:    msg.ChannelID,
		RoomID:       msg.RoomID,
		MsgID:        msg.MsgID,
		SenderID:     msg.SenderID,
		SenderName:   msg.SenderName,
		IsBot:        false,
		IsAtBot:      true,
		TimestampMs:  nowMs,
		ContentJSON:  string(blocksJSON),
		RenderedText: RenderBlocksText(msg.Blocks),
		Status:       "UNPROCESSED",
	}); err != nil {
		return fmt.Errorf("kernel: persist webui chat message: %w", err)
	}

	rt.Pipeline.broadcastTimelineAppend(msg)

	return rt.Pipeline.Bus().Submit(TriggerEvent{
		Source:     TriggerWebUI,
		ChannelID:  channelID,
		RoomID:     roomID,
		BypassGate: true,
		ReceivedAt: time.UnixMilli(nowMs),
	})
}

// HandleRunTrigger manually triggers a Run on the room's SingleFlightBus.
func (rt *Runtime) HandleRunTrigger(_ context.Context, params protocol.RunTriggerParams) error {
	roomID := strings.TrimSpace(params.RoomID)
	if roomID == "" {
		roomID = DefaultDebugRoomID
	}
	src := TriggerSource(strings.TrimSpace(params.Source))
	if src == "" {
		src = TriggerWebUI
	}
	var payload any
	if len(params.Payload) > 0 {
		_ = json.Unmarshal(params.Payload, &payload)
	}
	return rt.Pipeline.Bus().Submit(TriggerEvent{
		Source:     src,
		RoomID:     roomID,
		BypassGate: params.BypassGate,
		Payload:    payload,
		ReceivedAt: rt.clock(),
	})
}

// TriggerDreaming starts a non-blocking background Dreaming consolidation pass.
func (rt *Runtime) TriggerDreaming(roomID string) bool {
	if rt.Memory == nil {
		return false
	}
	rt.dreamMu.Lock()
	if rt.dreamStatus.Running {
		rt.dreamMu.Unlock()
		return false
	}
	rt.dreamStatus.Running = true
	rt.dreamMu.Unlock()

	rt.bgWG.Add(1)
	go func() {
		defer rt.bgWG.Done()
		defer func() {
			rt.dreamMu.Lock()
			rt.dreamStatus.Running = false
			rt.dreamStatus.LastRunAt = rt.clock().UTC().Format(time.RFC3339)
			rt.dreamMu.Unlock()
		}()

		runCtx, cancel := context.WithTimeout(rt.bgCtx, 2*time.Minute)
		defer cancel()

		rooms := rt.collectDreamRooms(runCtx, roomID)
		totalConsolidated := 0
		for _, rid := range rooms {
			rep, err := rt.Memory.RunDreaming(runCtx, rid, memory.DreamingOptions{
				NowMs: rt.clock().UnixMilli(),
			})
			if err != nil {
				rt.logger.Warn("manual dreaming failed", "room_id", rid, "err", err)
				continue
			}
			totalConsolidated += rep.SupersededCount + rep.ArchivedCount
		}
		rt.dreamMu.Lock()
		rt.dreamStatus.ConsolidatedCount += totalConsolidated
		rt.dreamMu.Unlock()
	}()
	return true
}

func (rt *Runtime) collectDreamRooms(ctx context.Context, explicitRoom string) []string {
	if r := strings.TrimSpace(explicitRoom); r != "" {
		return []string{r}
	}
	vecs, err := rt.Store.LoadActiveMemoryVectors(ctx, "")
	if err != nil {
		return []string{DefaultDebugRoomID}
	}
	seen := make(map[string]bool)
	var rooms []string
	for _, v := range vecs {
		if v.RoomID != "" && !seen[v.RoomID] {
			seen[v.RoomID] = true
			rooms = append(rooms, v.RoomID)
		}
	}
	if len(rooms) == 0 {
		return []string{DefaultDebugRoomID}
	}
	return rooms
}

// DreamingStatus returns the current Dreaming status snapshot.
func (rt *Runtime) DreamingStatus(ctx context.Context, roomID string) APIDreamingStatus {
	rt.dreamMu.Lock()
	st := rt.dreamStatus
	rt.dreamMu.Unlock()

	if roomID != "" {
		if unarchived, err := rt.Store.ListUnarchivedTimeline(ctx, roomID, 500); err == nil {
			st.PendingEpisodes = len(unarchived)
		}
	}
	return st
}

// Handler builds the unified HTTP handler mounting Channel Hub (/ws, /api/blobs),
// WebUI REST API (/api/...), and optional embedded SPA static handler (/).
func (rt *Runtime) Handler(webuiHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	rt.RegisterRoutes(mux)
	if webuiHandler != nil {
		mux.Handle("/", webuiHandler)
	}
	return mux
}

// Close gracefully shuts down the pipeline, background tasks, Channel Hub, MCP clients, and owned SQLite store.
func (rt *Runtime) Close(ctx context.Context) error {
	var errs []error
	if rt.Pipeline != nil {
		if err := rt.Pipeline.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	rt.bgCancel()
	done := make(chan struct{})
	go func() {
		rt.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	if rt.Hub != nil {
		rt.Hub.Close()
	}
	if rt.MCP != nil {
		if err := rt.MCP.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if rt.ownsStore && rt.Store != nil {
		if err := rt.Store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type blobAdapter struct {
	blobs *store.BlobStore
}

func (a blobAdapter) Put(ctx context.Context, mimeType string, r io.Reader) (channel.BlobMeta, error) {
	m, err := a.blobs.PutBlob(ctx, r, mimeType)
	if err != nil {
		return channel.BlobMeta{}, err
	}
	return channel.BlobMeta{BlobID: m.BlobID, MIMEType: m.MIMEType, SizeBytes: m.SizeBytes}, nil
}

func (a blobAdapter) Open(ctx context.Context, blobID string) (io.ReadCloser, channel.BlobMeta, error) {
	rc, m, err := a.blobs.GetBlob(ctx, blobID)
	if err != nil {
		if errors.Is(err, store.ErrBlobNotFound) {
			return nil, channel.BlobMeta{}, channel.ErrBlobNotFound
		}
		return nil, channel.BlobMeta{}, err
	}
	return rc, channel.BlobMeta{BlobID: m.BlobID, MIMEType: m.MIMEType, SizeBytes: m.SizeBytes}, nil
}

func (a blobAdapter) ReadBlob(ctx context.Context, blobID string) ([]byte, error) {
	rc, _, err := a.blobs.GetBlob(ctx, blobID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

type dynamicEmbedder struct {
	getAI func() *ai.Registry
}

func (d *dynamicEmbedder) Embed(ctx context.Context, req ai.EmbedRequest) (ai.EmbedResponse, error) {
	reg := d.getAI()
	if reg == nil {
		return ai.EmbedResponse{}, &ai.Error{Kind: ai.ErrInvalidArg, Message: "ai registry not configured"}
	}
	em, err := reg.Embedding(ai.SlotEmbedding)
	if err != nil {
		return ai.EmbedResponse{}, err
	}
	return em.Embed(ctx, req)
}

type dynamicMemeEmbedder struct {
	getAI func() *ai.Registry
}

func (d *dynamicMemeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	reg := d.getAI()
	if reg == nil {
		return nil, nil
	}
	em, err := reg.Embedding(ai.SlotEmbedding)
	if err != nil {
		return nil, nil
	}
	resp, err := em.Embed(ctx, ai.EmbedRequest{
		Texts:      []string{text},
		Dimensions: memory.DefaultVectorDim,
	})
	if err != nil || len(resp.Vectors) == 0 {
		return nil, err
	}
	return resp.Vectors[0], nil
}

type runtimeMemoryRecaller struct {
	engine *memory.Engine
	roomID string
	loc    *time.Location
}

func (r *runtimeMemoryRecaller) Recall(ctx context.Context, q tools.MemoryRecallQuery) ([]tools.RecalledMemory, error) {
	res, err := r.engine.ActiveRecall(ctx, memory.ActiveRecallRequest{
		RoomID:          r.roomID,
		Query:           q.Query,
		Entity:          q.Entity,
		TimeFrom:        parseFlexibleTimeMs(q.TimeFrom),
		TimeTo:          parseFlexibleTimeMs(q.TimeTo),
		IncludeArchived: q.IncludeArchived,
		Limit:           q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]tools.RecalledMemory, len(res.Hits))
	for i, h := range res.Hits {
		out[i] = tools.RecalledMemory{
			ID:        h.Memory.ID,
			Kind:      h.Memory.Kind,
			Content:   h.Memory.Content,
			TimeEvent: time.UnixMilli(h.Memory.TEventMs).In(r.loc).Format("2006-01-02"),
			Entities:  h.Memory.Entities,
			Score:     h.Score,
			Archived:  h.Memory.Archived,
		}
	}
	return out, nil
}

func parseFlexibleTimeMs(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func providerSecretKey(providerID string) string {
	return "provider:" + strings.TrimSpace(providerID)
}

func splitAPIKeys(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	}) {
		if k := strings.TrimSpace(part); k != "" {
			out = append(out, k)
		}
	}
	return out
}
