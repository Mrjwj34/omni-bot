package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"omni-bot/internal/ai"
	"omni-bot/internal/ai/fishaudio"
	"omni-bot/internal/ai/google"
	"omni-bot/internal/ai/openaiembed"
	"omni-bot/internal/ai/whisper"
	"omni-bot/internal/meme"
	"omni-bot/internal/memory"
	"omni-bot/internal/persona"
	"omni-bot/internal/persona/pad"
	"omni-bot/internal/protocol"
	"omni-bot/internal/store"
)

const maxMemeUploadBytes int64 = 16 << 20

// API DTOs below match web/src/lib/api/types.ts 1:1 with zero field-name mismatches.

type APIPADState struct {
	Value     pad.Vec3 `json:"value"`
	Feeling   string   `json:"feeling,omitempty"`
	Intensity float64  `json:"intensity,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

type APIPADTemperament struct {
	InitialBaseline pad.Vec3 `json:"initial_baseline"`
	Baseline        pad.Vec3 `json:"baseline"`
	Sensitivity     pad.Vec3 `json:"sensitivity"`
	HalfLifePMs     int64    `json:"half_life_p_ms"`
	HalfLifeAMs     int64    `json:"half_life_a_ms"`
	HalfLifeDMs     int64    `json:"half_life_d_ms"`
	DeadzoneRadius  float64  `json:"deadzone_radius"`
}

type APIAdapterInfo struct {
	ChannelID    string                `json:"channel_id"`
	Kind         string                `json:"kind"`
	Capabilities []protocol.Capability `json:"capabilities,omitempty"`
	Connected    bool                  `json:"connected"`
}

type APIDashboardMetrics struct {
	RunsToday     int      `json:"runs_today"`
	RepliesToday  int      `json:"replies_today"`
	MemoriesTotal int      `json:"memories_total"`
	MemesTotal    int      `json:"memes_total"`
	AvgLatencyMs  int64    `json:"avg_latency_ms"`
	CacheHitRatio *float64 `json:"cache_hit_ratio,omitempty"`
}

type APIRunTraceSummary struct {
	RunID        string   `json:"run_id"`
	RoomID       string   `json:"room_id"`
	Source       string   `json:"source"`
	Status       string   `json:"status"`
	GateScore    *float64 `json:"gate_score,omitempty"`
	StepsCount   int      `json:"steps_count"`
	DurationMs   int64    `json:"duration_ms"`
	TokensIn     int      `json:"tokens_in,omitempty"`
	TokensOut    int      `json:"tokens_out,omitempty"`
	CachedTokens int      `json:"cached_tokens,omitempty"`
	StartedAt    string   `json:"started_at"`
	Summary      string   `json:"summary,omitempty"`
}

type APIRunTraceDetail struct {
	APIRunTraceSummary
	PADBefore    *pad.Vec3                 `json:"pad_before,omitempty"`
	PADAfter     *pad.Vec3                 `json:"pad_after,omitempty"`
	InnerFeeling string                    `json:"inner_feeling,omitempty"`
	MemoryHits   []TraceMemoryHit          `json:"memory_hits,omitempty"`
	ToolCalls    []TraceToolCall           `json:"tool_calls,omitempty"`
	Actions      []protocol.OutboundAction `json:"actions,omitempty"`
	Steps        []TraceStep               `json:"steps"`
	Error        string                    `json:"error,omitempty"`
}

type APIDashboardResponse struct {
	Status       string               `json:"status"`
	UptimeSec    int64                `json:"uptime_sec"`
	PAD          APIPADState          `json:"pad"`
	Adapters     []APIAdapterInfo     `json:"adapters"`
	Metrics      APIDashboardMetrics  `json:"metrics"`
	RecentTraces []APIRunTraceSummary `json:"recent_traces"`
}

type APIMemoryItem struct {
	ID           string   `json:"id"`
	RoomID       string   `json:"room_id,omitempty"`
	Content      string   `json:"content"`
	Kind         string   `json:"kind"`
	Entities     []string `json:"entities"`
	Strength     float64  `json:"strength"`
	RecallCount  int      `json:"recall_count"`
	TEvent       string   `json:"t_event,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type APIMemorySearchResponse struct {
	Query     string          `json:"query"`
	Items     []APIMemoryItem `json:"items"`
	ElapsedMs int64           `json:"elapsed_ms"`
}

type APIDreamingStatus struct {
	LastRunAt         string `json:"last_run_at,omitempty"`
	NextRunAt         string `json:"next_run_at,omitempty"`
	PendingEpisodes   int    `json:"pending_episodes"`
	ConsolidatedCount int    `json:"consolidated_count"`
	Running           bool   `json:"running"`
}

type APIPersonaTrait struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Weight    float64 `json:"weight"`
	UpdatedAt string  `json:"updated_at"`
}

type APIPersonaSnapshot struct {
	Version     int64             `json:"version"`
	CreatedAt   string            `json:"created_at"`
	Reason      string            `json:"reason"`
	CoreBeliefs string            `json:"core_beliefs"`
	Traits      []APIPersonaTrait `json:"traits"`
}

type APIPersonaResponse struct {
	CoreBeliefs string               `json:"core_beliefs"`
	Traits      []APIPersonaTrait    `json:"traits"`
	Temperament APIPADTemperament    `json:"temperament"`
	PAD         APIPADState          `json:"pad"`
	Snapshots   []APIPersonaSnapshot `json:"snapshots"`
}

type APIMemeItem struct {
	ID          string   `json:"id"`
	Hash        string   `json:"hash"`
	BlobID      string   `json:"blob_id"`
	ThumbURL    string   `json:"thumb_url"`
	ImageURL    string   `json:"image_url"`
	OverlayText string   `json:"overlay_text,omitempty"`
	Description string   `json:"description"`
	EmotionTags []string `json:"emotion_tags"`
	SceneTags   []string `json:"scene_tags"`
	UseCount    int      `json:"use_count"`
	Blocked     bool     `json:"blocked"`
	CreatedAt   string   `json:"created_at"`
}

type APIMemeListResponse struct {
	Items []APIMemeItem `json:"items"`
	Total int           `json:"total"`
}

type APIProviderConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url"`
	SecretKey  string `json:"secret_key,omitempty"`
	HasAPIKey  bool   `json:"has_api_key"`
	APIKeyHint string `json:"api_key_hint,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type APIModelSlotCapabilities struct {
	Vision  bool `json:"vision,omitempty"`
	AudioIn bool `json:"audio_in,omitempty"`
	VideoIn bool `json:"video_in,omitempty"`
}

type APIModelSlotConfig struct {
	Slot         string                    `json:"slot"`
	ProviderID   string                    `json:"provider_id"`
	ModelID      string                    `json:"model_id"`
	Capabilities *APIModelSlotCapabilities `json:"capabilities,omitempty"`
}

type APIMCPServerConfig struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Transport  string   `json:"transport"`
	Endpoint   string   `json:"endpoint,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Enabled    bool     `json:"enabled"`
	ToolsCount int      `json:"tools_count,omitempty"`
	Status     string   `json:"status,omitempty"`
}

type APIFeaturesConfig struct {
	MemoryEnabled      bool `json:"memory_enabled"`
	EmotionEnabled     bool `json:"emotion_enabled"`
	MemeCollectEnabled bool `json:"meme_collect_enabled"`
	BashEnabled        bool `json:"bash_enabled"`
}

type APIRuntimeConfig struct {
	Providers  []APIProviderConfig  `json:"providers"`
	ModelSlots []APIModelSlotConfig `json:"model_slots"`
	MCPServers []APIMCPServerConfig `json:"mcp_servers"`
	Features   APIFeaturesConfig    `json:"features"`
}

type APIProviderUpdateInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	SecretKey string `json:"secret_key,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type APIRuntimeConfigPatch struct {
	Providers  []APIProviderUpdateInput `json:"providers,omitempty"`
	ModelSlots []APIModelSlotConfig     `json:"model_slots,omitempty"`
	MCPServers []APIMCPServerConfig     `json:"mcp_servers,omitempty"`
	Features   *struct {
		MemoryEnabled      *bool `json:"memory_enabled,omitempty"`
		EmotionEnabled     *bool `json:"emotion_enabled,omitempty"`
		MemeCollectEnabled *bool `json:"meme_collect_enabled,omitempty"`
		BashEnabled        *bool `json:"bash_enabled,omitempty"`
		BashToolsEnabled   *bool `json:"bash_tools_enabled,omitempty"`
	} `json:"features,omitempty"`
}

type APITimelineMessage struct {
	MsgID        string                  `json:"msg_id"`
	ChannelID    string                  `json:"channel_id"`
	RoomID       string                  `json:"room_id"`
	SenderID     string                  `json:"sender_id"`
	SenderName   string                  `json:"sender_name"`
	IsBot        bool                    `json:"is_bot"`
	TimestampMs  int64                   `json:"timestamp_ms"`
	Blocks       []protocol.ContentBlock `json:"blocks"`
	ReplyToMsgID string                  `json:"reply_to_msg_id,omitempty"`
}

// RegisterRoutes mounts Channel Hub endpoints and all WebUI HTTP REST API routes on mux.
func (rt *Runtime) RegisterRoutes(mux *http.ServeMux) {
	if rt.Hub != nil {
		rt.Hub.RegisterRoutes(mux)
	}

	// Dashboard & live PAD history
	mux.HandleFunc("GET /api/dashboard", rt.handleGetDashboard)
	mux.HandleFunc("GET /api/pad/history", rt.handleGetPADHistory)

	// Run traces
	mux.HandleFunc("GET /api/traces", rt.handleListTraces)
	mux.HandleFunc("GET /api/traces/{id}", rt.handleGetTrace)

	// Memories, entity aliases & Dreaming
	mux.HandleFunc("GET /api/memories", rt.handleListMemories)
	mux.HandleFunc("GET /api/memories/search", rt.handleSearchMemories)
	mux.HandleFunc("POST /api/memories/dream", rt.handleTriggerDreaming)
	mux.HandleFunc("GET /api/memories/aliases", rt.handleListAliases)
	mux.HandleFunc("PUT /api/memories/aliases", rt.handleUpsertAlias)
	mux.HandleFunc("POST /api/memories/aliases", rt.handleUpsertAlias)
	mux.HandleFunc("DELETE /api/memories/aliases", rt.handleDeleteAlias)

	// Persona, traits, temperament, reflection & snapshots
	mux.HandleFunc("GET /api/persona", rt.handleGetPersona)
	mux.HandleFunc("PATCH /api/persona", rt.handleUpdatePersona)
	mux.HandleFunc("PUT /api/persona", rt.handleUpdatePersona)
	mux.HandleFunc("PUT /api/persona/traits", rt.handleUpdatePersona)
	mux.HandleFunc("PATCH /api/persona/traits", rt.handleUpdatePersona)
	mux.HandleFunc("PUT /api/persona/temperament", rt.handleUpdateTemperament)
	mux.HandleFunc("POST /api/persona/reflect", rt.handleReflectPersona)
	mux.HandleFunc("GET /api/persona/snapshots", rt.handleListPersonaSnapshots)
	mux.HandleFunc("GET /api/persona/snapshots/diff", rt.handleDiffPersonaSnapshots)
	mux.HandleFunc("POST /api/persona/snapshots/{id}/rollback", rt.handleRollbackPersonaSnapshot)

	// Memes & binary serving
	mux.HandleFunc("GET /api/memes", rt.handleListMemes)
	mux.HandleFunc("POST /api/memes", rt.handleUploadMeme)
	mux.HandleFunc("GET /api/memes/{hash}/thumb", rt.handleGetMemeThumb)
	mux.HandleFunc("GET /api/memes/{hash}/image", rt.handleGetMemeImage)
	mux.HandleFunc("PATCH /api/memes/{id}", rt.handleUpdateMeme)
	mux.HandleFunc("DELETE /api/memes/{id}", rt.handleDeleteMeme)

	// Runtime config (strictly redacted), MCP reload & write-only secrets
	mux.HandleFunc("GET /api/config", rt.handleGetConfig)
	mux.HandleFunc("PATCH /api/config", rt.handleUpdateConfig)
	mux.HandleFunc("PUT /api/config", rt.handleUpdateConfig)
	mux.HandleFunc("POST /api/mcp/{id}/reload", rt.handleReloadMCP)
	mux.HandleFunc("GET /api/secrets", rt.handleListSecrets)
	mux.HandleFunc("PUT /api/secrets/{key}", rt.handleSetSecret)
	mux.HandleFunc("DELETE /api/secrets/{key}", rt.handleDeleteSecret)

	// Built-in Chat Debug Console & manual run trigger
	mux.HandleFunc("GET /api/chat/timeline", rt.handleGetTimeline)
	mux.HandleFunc("POST /api/chat/send", rt.handleChatSend)
	mux.HandleFunc("POST /api/runs/trigger", rt.handleRunTrigger)
}

func (rt *Runtime) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = store.DefaultPersonaRoomID
	}

	padState := rt.currentAPIPAD(ctx, roomID)

	adapters := make([]APIAdapterInfo, 0)
	if rt.Hub != nil {
		for _, a := range rt.Hub.Adapters() {
			adapters = append(adapters, APIAdapterInfo{
				ChannelID:    a.ChannelID,
				Kind:         a.Kind,
				Capabilities: a.Capabilities,
				Connected:    true,
			})
		}
	}

	rawTraces, err := rt.Store.ListRunTraces(ctx, "", 200)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	now := rt.clock()
	dayStartMs := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()

	var (
		runsToday    int
		repliesToday int
		totalDurMs   int64
		durCount     int64
		totalIn      int
		totalCached  int
	)
	recent := make([]APIRunTraceSummary, 0, min(10, len(rawTraces)))
	for i, tr := range rawTraces {
		sum := toTraceSummary(tr)
		if i < 10 {
			recent = append(recent, sum)
		}
		if tr.StartedAtMs >= dayStartMs {
			runsToday++
			if tr.Status == string(StatusCompleted) {
				repliesToday++
			}
		}
		if sum.DurationMs > 0 {
			totalDurMs += sum.DurationMs
			durCount++
		}
		totalIn += sum.TokensIn
		totalCached += sum.CachedTokens
	}

	var avgLatency int64
	if durCount > 0 {
		avgLatency = totalDurMs / durCount
	}
	var cacheRatio *float64
	if totalIn > 0 {
		r := float64(totalCached) / float64(totalIn)
		cacheRatio = &r
	}

	memTotal := 0
	if rt.Memory != nil {
		memTotal = rt.Memory.VectorCount()
	}
	memesTotal := 0
	if activeMemes, err := rt.Store.ListActiveMemes(ctx); err == nil {
		memesTotal = len(activeMemes)
	}

	status := "ok"
	if reg := rt.AIRegistry(); reg == nil {
		status = "degraded"
	}

	writeAPIJSON(w, http.StatusOK, APIDashboardResponse{
		Status:    status,
		UptimeSec: max(int64(0), int64(now.Sub(rt.startedAt).Seconds())),
		PAD:       padState,
		Adapters:  adapters,
		Metrics: APIDashboardMetrics{
			RunsToday:     runsToday,
			RepliesToday:  repliesToday,
			MemoriesTotal: memTotal,
			MemesTotal:    memesTotal,
			AvgLatencyMs:  avgLatency,
			CacheHitRatio: cacheRatio,
		},
		RecentTraces: recent,
	})
}

func (rt *Runtime) handleGetPADHistory(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = store.DefaultPersonaRoomID
	}
	limit := parseLimit(r, 50, 500)

	rows, err := rt.Store.ListPADHistory(r.Context(), roomID, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		ID          int64    `json:"id"`
		RoomID      string   `json:"room_id"`
		RunID       string   `json:"run_id,omitempty"`
		Value       pad.Vec3 `json:"value"`
		Delta       pad.Vec3 `json:"delta"`
		PrototypeID string   `json:"prototype_id,omitempty"`
		TimestampMs int64    `json:"timestamp_ms"`
		CreatedAt   string   `json:"created_at"`
	}
	items := make([]item, 0, len(rows))
	for _, row := range rows {
		items = append(items, item{
			ID:          row.ID,
			RoomID:      row.RoomID,
			RunID:       row.RunID,
			Value:       pad.Vec3{P: row.P, A: row.A, D: row.D},
			Delta:       pad.Vec3{P: row.DeltaP, A: row.DeltaA, D: row.DeltaD},
			PrototypeID: row.PrototypeID,
			TimestampMs: row.CreatedAtMs,
			CreatedAt:   msToRFC3339(row.CreatedAtMs),
		})
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (rt *Runtime) handleListTraces(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := parseLimit(r, 50, 200)

	fetchLimit := limit
	if statusFilter != "" {
		fetchLimit = min(500, limit*4)
	}
	rows, err := rt.Store.ListRunTraces(r.Context(), roomID, fetchLimit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]APIRunTraceSummary, 0, min(limit, len(rows)))
	for _, row := range rows {
		if statusFilter != "" && row.Status != statusFilter {
			continue
		}
		items = append(items, toTraceSummary(row))
		if len(items) >= limit {
			break
		}
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (rt *Runtime) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" {
		writeAPIError(w, http.StatusBadRequest, "run_id is required")
		return
	}
	tr, ok, err := rt.Store.GetRunTrace(r.Context(), runID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "run trace not found")
		return
	}
	writeAPIJSON(w, http.StatusOK, toTraceDetail(tr))
}

func (rt *Runtime) handleListMemories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	kindFilter := strings.TrimSpace(r.URL.Query().Get("kind"))
	limit := parseLimit(r, 50, 200)

	vecs, err := rt.Store.LoadActiveMemoryVectors(ctx, roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nowMs := rt.clock().UnixMilli()
	items := make([]APIMemoryItem, 0, min(limit, len(vecs)))
	for _, mv := range vecs {
		if kindFilter != "" && !memoryKindMatches(mv.Kind, kindFilter) {
			continue
		}
		m, ok, err := rt.Store.GetMemory(ctx, mv.ID)
		if err != nil || !ok {
			continue
		}
		items = append(items, toAPIMemory(m, nowMs, rt.location))
		if len(items) >= limit {
			break
		}
	}

	writeAPIJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"dreaming": rt.DreamingStatus(ctx, roomID),
	})
}

func (rt *Runtime) handleSearchMemories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	limit := parseLimit(r, 20, 100)

	t0 := time.Now()
	nowMs := rt.clock().UnixMilli()
	items := make([]APIMemoryItem, 0)

	if q == "" {
		vecs, err := rt.Store.LoadActiveMemoryVectors(ctx, roomID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, mv := range vecs {
			m, ok, err := rt.Store.GetMemory(ctx, mv.ID)
			if err != nil || !ok {
				continue
			}
			items = append(items, toAPIMemory(m, nowMs, rt.location))
			if len(items) >= limit {
				break
			}
		}
	} else if rt.Memory != nil {
		res, err := rt.Memory.ActiveRecall(ctx, memory.ActiveRecallRequest{
			RoomID:          roomID,
			Query:           q,
			IncludeArchived: true,
			Limit:           limit,
			NowMs:           nowMs,
		})
		if err != nil {
			// Fallback to pure FTS if embedder is not configured yet.
			hits, ftsErr := rt.Store.SearchMemoriesFTS(ctx, roomID, q, limit, true)
			if ftsErr != nil {
				writeAPIError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, h := range hits {
				items = append(items, toAPIMemory(h.Memory, nowMs, rt.location))
			}
		} else {
			for _, h := range res.Hits {
				items = append(items, toAPIMemory(h.Memory, nowMs, rt.location))
			}
		}
	}

	writeAPIJSON(w, http.StatusOK, APIMemorySearchResponse{
		Query:     q,
		Items:     items,
		ElapsedMs: time.Since(t0).Milliseconds(),
	})
}

func (rt *Runtime) handleTriggerDreaming(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RoomID string `json:"room_id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body)
	}
	accepted := rt.TriggerDreaming(body.RoomID)
	writeAPIJSON(w, http.StatusAccepted, map[string]bool{"accepted": accepted})
}

func (rt *Runtime) handleListAliases(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = DefaultDebugRoomID
	}
	aliases, err := rt.Store.ListEntityAliases(r.Context(), roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if aliases == nil {
		aliases = []store.EntityAlias{}
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": aliases})
}

func (rt *Runtime) handleUpsertAlias(w http.ResponseWriter, r *http.Request) {
	var alias store.EntityAlias
	if err := decodeJSONBody(r, &alias); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(alias.RoomID) == "" {
		alias.RoomID = DefaultDebugRoomID
	}
	alias.UpdatedAtMs = rt.clock().UnixMilli()
	if err := rt.Store.UpsertEntityAlias(r.Context(), alias); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, alias)
}

func (rt *Runtime) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	alias := strings.TrimSpace(r.URL.Query().Get("alias"))
	if roomID == "" || alias == "" {
		writeAPIError(w, http.StatusBadRequest, "room_id and alias query params are required")
		return
	}
	if err := rt.Store.DeleteEntityAlias(r.Context(), roomID, alias); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (rt *Runtime) handleGetPersona(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = store.DefaultPersonaRoomID
	}
	resp, err := rt.buildPersonaResponse(r.Context(), roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (rt *Runtime) handleUpdatePersona(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = store.DefaultPersonaRoomID
	}

	var body struct {
		CoreBeliefs *string `json:"core_beliefs,omitempty"`
		Traits      []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"traits,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "manual_update"
	}

	if body.CoreBeliefs != nil {
		core, ok, err := rt.Store.GetPersonaCore(ctx, roomID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			core = store.PersonaCore{
				RoomID:      roomID,
				Temperament: persona.TemperamentToStore(pad.DefaultTemperament()),
			}
		}
		core.Beliefs = strings.TrimSpace(*body.CoreBeliefs)
		core.UpdatedAtMs = rt.clock().UnixMilli()
		snapReason := reason
		if len(body.Traits) > 0 {
			snapReason = ""
		}
		if err := rt.Store.SetPersonaCore(ctx, core, snapReason); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if len(body.Traits) > 0 {
		if len(body.Traits) < persona.MinTraits || len(body.Traits) > persona.MaxTraits {
			writeAPIError(w, http.StatusBadRequest, persona.ErrTraitCountOutOfBounds.Error())
			return
		}
		nowMs := rt.clock().UnixMilli()
		next := make([]store.PersonaTrait, len(body.Traits))
		for i, tr := range body.Traits {
			if err := persona.ValidateFirstPersonTrait(tr.Text); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			id := strings.TrimSpace(tr.ID)
			if id == "" {
				id = fmt.Sprintf("trait_%d_%d", nowMs, i+1)
			}
			next[i] = store.PersonaTrait{
				ID:          id,
				Content:     strings.TrimSpace(tr.Text),
				Position:    i + 1,
				CreatedAtMs: nowMs,
				UpdatedAtMs: nowMs,
			}
		}
		if _, err := rt.Store.ReplacePersonaTraits(ctx, roomID, next, reason); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	resp, err := rt.buildPersonaResponse(ctx, roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (rt *Runtime) handleUpdateTemperament(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = store.DefaultPersonaRoomID
	}

	var input APIPADTemperament
	if err := decodeJSONBody(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	core, ok, err := rt.Store.GetPersonaCore(ctx, roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		core = store.PersonaCore{
			RoomID:  roomID,
			Beliefs: persona.DefaultBeliefs(),
		}
	}
	core.Temperament = apiTemperamentToStore(input)
	core.UpdatedAtMs = rt.clock().UnixMilli()

	if err := rt.Store.SetPersonaCore(ctx, core, "temperament_update"); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp, err := rt.buildPersonaResponse(ctx, roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (rt *Runtime) handleReflectPersona(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		RoomID     string   `json:"room_id"`
		Highlights []string `json:"highlights,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body)
	}
	roomID := strings.TrimSpace(body.RoomID)
	if roomID == "" {
		roomID = store.DefaultPersonaRoomID
	}

	highlights := body.Highlights
	if len(highlights) == 0 {
		vecs, err := rt.Store.LoadActiveMemoryVectors(ctx, "")
		if err == nil {
			for i := 0; i < len(vecs) && len(highlights) < 20; i++ {
				if m, ok, err := rt.Store.GetMemory(ctx, vecs[i].ID); err == nil && ok {
					highlights = append(highlights, memory.FormatMemoryLine(m, rt.location))
				}
			}
		}
	}

	reg := rt.AIRegistry()
	if reg == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "ai registry not configured")
		return
	}
	lm, err := reg.Language(ai.SlotChat)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, fmt.Sprintf("chat model not configured: %v", err))
		return
	}

	refRes, err := rt.Persona.ReflectTraits(ctx, roomID, highlights, lm)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pResp, err := rt.buildPersonaResponse(ctx, roomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"reflection": refRes,
		"persona":    pResp,
	})
}

func (rt *Runtime) handleListPersonaSnapshots(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 30, 200)
	snaps, err := rt.Store.ListPersonaSnapshots(r.Context(), limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]APIPersonaSnapshot, 0, len(snaps))
	for _, s := range snaps {
		items = append(items, toAPISnapshot(s))
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (rt *Runtime) handleDiffPersonaSnapshots(w http.ResponseWriter, r *http.Request) {
	fromID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("from")), 10, 64)
	if err != nil || fromID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "valid 'from' snapshot id is required")
		return
	}
	toID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("to")), 10, 64)
	if err != nil || toID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "valid 'to' snapshot id is required")
		return
	}
	fromSnap, ok, err := rt.Store.GetPersonaSnapshot(r.Context(), fromID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("snapshot %d not found", fromID))
		return
	}
	toSnap, ok, err := rt.Store.GetPersonaSnapshot(r.Context(), toID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("snapshot %d not found", toID))
		return
	}
	writeAPIJSON(w, http.StatusOK, persona.DiffSnapshots(fromSnap, toSnap))
}

func (rt *Runtime) handleRollbackPersonaSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || snapID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "valid snapshot id is required")
		return
	}
	res, err := rt.Persona.RollbackToSnapshot(r.Context(), snapID)
	if err != nil {
		if errors.Is(err, persona.ErrSnapshotNotFound) {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pResp, err := rt.buildPersonaResponse(r.Context(), res.Core.RoomID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"rollback": res,
		"persona":  pResp,
	})
}

func (rt *Runtime) handleListMemes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	limit := parseLimit(r, 60, 300)

	var memes []store.Meme
	var err error
	if q != "" {
		memes, err = rt.Store.SearchMemes(ctx, q, limit*2)
	} else {
		memes, err = rt.Store.ListActiveMemes(ctx)
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]APIMemeItem, 0, min(limit, len(memes)))
	for _, m := range memes {
		dto := toAPIMeme(m)
		if tag != "" && !memeMatchesTag(dto, tag) {
			continue
		}
		items = append(items, dto)
		if len(items) >= limit {
			break
		}
	}

	writeAPIJSON(w, http.StatusOK, APIMemeListResponse{
		Items: items,
		Total: len(memes),
	})
}

func (rt *Runtime) handleUploadMeme(w http.ResponseWriter, r *http.Request) {
	if rt.Memes == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "meme vault not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMemeUploadBytes)

	var (
		raw         []byte
		ext         string
		hint        string
		overlayText string
		emotionTags string
		sceneTags   string
	)
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxMemeUploadBytes); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid multipart upload")
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "missing file field")
			return
		}
		defer func() { _ = file.Close() }()
		raw, err = io.ReadAll(file)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "failed to read file")
			return
		}
		if hdr != nil {
			ext = filepath.Ext(hdr.Filename)
		}
		hint = strings.TrimSpace(r.FormValue("hint"))
		if hint == "" {
			hint = strings.TrimSpace(r.FormValue("description"))
		}
		overlayText = strings.TrimSpace(r.FormValue("overlay_text"))
		emotionTags = strings.TrimSpace(r.FormValue("emotion_tags"))
		sceneTags = strings.TrimSpace(r.FormValue("scene_tags"))
	} else {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		hint = strings.TrimSpace(r.URL.Query().Get("hint"))
	}
	if len(raw) == 0 {
		writeAPIError(w, http.StatusBadRequest, "empty meme image")
		return
	}

	res, err := rt.Memes.Ingest(r.Context(), raw, ext, hint)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if res.Meme.Hash != "" && (overlayText != "" || emotionTags != "" || sceneTags != "") {
		patch := meme.ImpressionPatch{}
		if overlayText != "" {
			patch.OverlayText = &overlayText
		}
		if emotionTags != "" {
			patch.Emotions = &emotionTags
		}
		if sceneTags != "" {
			patch.Scenes = &sceneTags
		}
		if updated, uerr := rt.Memes.UpdateImpression(r.Context(), res.Meme.Hash, patch); uerr == nil {
			res.Meme = updated
		}
	}

	var item *APIMemeItem
	if res.Meme.Hash != "" {
		dto := toAPIMeme(res.Meme)
		item = &dto
	}
	writeAPIJSON(w, http.StatusCreated, map[string]any{
		"status":       res.Status,
		"duplicate_of": res.DuplicateOf,
		"item":         item,
	})
}

func (rt *Runtime) handleGetMemeThumb(w http.ResponseWriter, r *http.Request) {
	rt.serveMemeAsset(w, r, true)
}

func (rt *Runtime) handleGetMemeImage(w http.ResponseWriter, r *http.Request) {
	rt.serveMemeAsset(w, r, false)
}

func (rt *Runtime) serveMemeAsset(w http.ResponseWriter, r *http.Request, thumb bool) {
	if rt.Blobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "blob store not configured")
		return
	}
	hash := strings.TrimSpace(r.PathValue("hash"))
	if hash == "" {
		writeAPIError(w, http.StatusBadRequest, "meme hash is required")
		return
	}
	m, ok, err := rt.Store.GetMeme(r.Context(), hash)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		m, ok, err = rt.Store.FindMemeByPrefix(r.Context(), hash)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeAPIError(w, http.StatusNotFound, "meme not found")
			return
		}
	}

	rel := m.RelPath
	if thumb && strings.TrimSpace(m.ThumbRelPath) != "" {
		rel = m.ThumbRelPath
	}
	if rel == "" {
		writeAPIError(w, http.StatusNotFound, "meme file not found")
		return
	}

	rc, err := rt.Blobs.OpenMemeFile(r.Context(), rel)
	if err != nil {
		if thumb && m.RelPath != "" && m.RelPath != rel {
			rc, err = rt.Blobs.OpenMemeFile(r.Context(), m.RelPath)
		}
		if err != nil {
			if errors.Is(err, store.ErrBlobNotFound) {
				writeAPIError(w, http.StatusNotFound, "meme file not found")
				return
			}
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	defer func() { _ = rc.Close() }()

	head := make([]byte, 512)
	n, readErr := io.ReadFull(rc, head)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		writeAPIError(w, http.StatusInternalServerError, "read meme asset failed")
		return
	}
	head = head[:n]

	w.Header().Set("Content-Type", http.DetectContentType(head))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(head)
	_, _ = io.Copy(w, rc)
}

func (rt *Runtime) handleUpdateMeme(w http.ResponseWriter, r *http.Request) {
	if rt.Memes == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "meme vault not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "meme id is required")
		return
	}
	var body struct {
		OverlayText *string   `json:"overlay_text,omitempty"`
		Description *string   `json:"description,omitempty"`
		EmotionTags *[]string `json:"emotion_tags,omitempty"`
		SceneTags   *[]string `json:"scene_tags,omitempty"`
		Blocked     *bool     `json:"blocked,omitempty"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	if body.Blocked != nil && *body.Blocked {
		deleted, err := rt.Memes.Delete(r.Context(), id)
		if err != nil {
			if errors.Is(err, meme.ErrMemeNotFound) {
				writeAPIError(w, http.StatusNotFound, err.Error())
				return
			}
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAPIJSON(w, http.StatusOK, toAPIMeme(deleted))
		return
	}

	patch := meme.ImpressionPatch{
		OverlayText: body.OverlayText,
		Description: body.Description,
	}
	if body.EmotionTags != nil {
		joined := strings.Join(*body.EmotionTags, ", ")
		patch.Emotions = &joined
	}
	if body.SceneTags != nil {
		joined := strings.Join(*body.SceneTags, ", ")
		patch.Scenes = &joined
	}

	updated, err := rt.Memes.UpdateImpression(r.Context(), id, patch)
	if err != nil {
		if errors.Is(err, meme.ErrMemeNotFound) {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, toAPIMeme(updated))
}

func (rt *Runtime) handleDeleteMeme(w http.ResponseWriter, r *http.Request) {
	if rt.Memes == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "meme vault not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "meme id is required")
		return
	}
	if _, err := rt.Memes.Delete(r.Context(), id); err != nil {
		if errors.Is(err, meme.ErrMemeNotFound) {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (rt *Runtime) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	resp, err := rt.buildAPIRuntimeConfig(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (rt *Runtime) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var patch APIRuntimeConfigPatch
	if err := decodeJSONBody(r, &patch); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg, err := rt.Store.LoadRuntimeConfig(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Apply providers update, persisting any write-only api_key into the secrets table.
	if patch.Providers != nil {
		existingByID := make(map[string]store.ProviderConfig, len(cfg.Providers))
		for _, p := range cfg.Providers {
			existingByID[p.ID] = p
		}
		nextProviders := make([]store.ProviderConfig, 0, len(patch.Providers))
		for i, in := range patch.Providers {
			id := strings.TrimSpace(in.ID)
			if id == "" {
				id = slugID(in.Name, fmt.Sprintf("provider-%d", i+1))
			}
			prev := existingByID[id]
			secKey := strings.TrimSpace(in.SecretKey)
			if secKey == "" {
				secKey = prev.SecretKey
			}
			if secKey == "" {
				secKey = providerSecretKey(id)
			}
			if rawKey := strings.TrimSpace(in.APIKey); rawKey != "" {
				if err := rt.Store.SetSecret(ctx, secKey, rawKey); err != nil {
					writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("save provider secret: %v", err))
					return
				}
			}
			nextProviders = append(nextProviders, store.ProviderConfig{
				ID:        id,
				Name:      strings.TrimSpace(in.Name),
				Kind:      strings.TrimSpace(in.Kind),
				BaseURL:   strings.TrimSpace(in.BaseURL),
				SecretKey: secKey,
				Headers:   prev.Headers,
				ProxyURL:  prev.ProxyURL,
				Enabled:   in.Enabled,
			})
		}
		cfg.Providers = nextProviders
	}

	// Apply model_slots update -> normalize into store.Models + store.Slots.
	if patch.ModelSlots != nil {
		providerKindByID := make(map[string]string, len(cfg.Providers))
		for _, p := range cfg.Providers {
			providerKindByID[p.ID] = p.Kind
		}
		nextModels := make([]store.ModelConfig, 0, len(patch.ModelSlots))
		var nextSlots store.SlotBindings
		for _, ms := range patch.ModelSlots {
			slot := strings.ToLower(strings.TrimSpace(ms.Slot))
			if slot == "" || strings.TrimSpace(ms.ModelID) == "" || strings.TrimSpace(ms.ProviderID) == "" {
				continue
			}
			logicalID := "slot:" + slot
			caps := store.ModelCapabilities{ToolCalls: slot == "chat" || slot == "summary" || slot == "vision"}
			if ms.Capabilities != nil {
				caps.Vision = ms.Capabilities.Vision
				caps.AudioIn = ms.Capabilities.AudioIn
				caps.VideoIn = ms.Capabilities.VideoIn
			}
			nextModels = append(nextModels, store.ModelConfig{
				ID:           logicalID,
				ProviderID:   strings.TrimSpace(ms.ProviderID),
				Protocol:     inferProtocolForSlot(slot, providerKindByID[ms.ProviderID]),
				ModelID:      strings.TrimSpace(ms.ModelID),
				Capabilities: caps,
			})
			switch slot {
			case "chat":
				nextSlots.Chat = logicalID
			case "decision":
				nextSlots.Decision = logicalID
			case "summary":
				nextSlots.Summary = logicalID
			case "vision":
				nextSlots.Vision = logicalID
			case "embedding":
				nextSlots.Embedding = logicalID
			case "tts":
				nextSlots.TTS = logicalID
			case "stt", "asr":
				nextSlots.STT = logicalID
			}
		}
		cfg.Models = nextModels
		cfg.Slots = nextSlots
	}

	// Apply MCP servers update.
	if patch.MCPServers != nil {
		existingByID := make(map[string]store.MCPServerConfig, len(cfg.MCPServers))
		for _, s := range cfg.MCPServers {
			existingByID[s.ID] = s
		}
		nextMCP := make([]store.MCPServerConfig, 0, len(patch.MCPServers))
		for i, in := range patch.MCPServers {
			id := strings.TrimSpace(in.ID)
			if id == "" {
				id = slugID(in.Name, fmt.Sprintf("mcp-%d", i+1))
			}
			prev := existingByID[id]
			nextMCP = append(nextMCP, store.MCPServerConfig{
				ID:        id,
				Name:      strings.TrimSpace(in.Name),
				Transport: strings.TrimSpace(in.Transport),
				URL:       strings.TrimSpace(in.Endpoint),
				Command:   strings.TrimSpace(in.Command),
				Args:      slices.Clone(in.Args),
				SecretKey: prev.SecretKey,
				Env:       prev.Env,
				Enabled:   in.Enabled,
			})
		}
		cfg.MCPServers = nextMCP
	}

	// Apply feature flag updates.
	if patch.Features != nil {
		if patch.Features.MemoryEnabled != nil {
			cfg.Features.MemoryEnabled = *patch.Features.MemoryEnabled
		}
		if patch.Features.EmotionEnabled != nil {
			cfg.Features.EmotionEnabled = *patch.Features.EmotionEnabled
		}
		if patch.Features.MemeCollectEnabled != nil {
			cfg.Features.MemeCollectEnabled = *patch.Features.MemeCollectEnabled
		}
		if patch.Features.BashEnabled != nil {
			cfg.Features.BashToolsEnabled = *patch.Features.BashEnabled
		} else if patch.Features.BashToolsEnabled != nil {
			cfg.Features.BashToolsEnabled = *patch.Features.BashToolsEnabled
		}
	}

	if err := rt.Store.SaveRuntimeConfig(ctx, cfg); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Atomically hot-reload AI registry, feature flags (Bash toggle), and MCP connections.
	if err := rt.Reload(ctx); err != nil {
		rt.logger.Warn("runtime hot-reload completed with warnings", "err", err)
	}

	resp, err := rt.buildAPIRuntimeConfig(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (rt *Runtime) handleReloadMCP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	err := rt.Reload(r.Context())
	resp := map[string]any{
		"reloaded": true,
		"id":       id,
	}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (rt *Runtime) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	statuses, err := rt.Store.ListSecretStatus(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if statuses == nil {
		statuses = []store.SecretStatus{}
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": statuses})
}

func (rt *Runtime) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "secret key is required")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rt.Store.SetSecret(r.Context(), key, body.Value); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Trigger hot-reload so updated provider/MCP keys take effect immediately.
	_ = rt.Reload(r.Context())

	statuses, err := rt.Store.ListSecretStatus(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, st := range statuses {
		if st.Key == key {
			writeAPIJSON(w, http.StatusOK, st)
			return
		}
	}
	writeAPIJSON(w, http.StatusOK, store.SecretStatus{Key: key, Configured: true})
}

func (rt *Runtime) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "secret key is required")
		return
	}
	if err := rt.Store.DeleteSecret(r.Context(), key); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = rt.Reload(r.Context())
	writeAPIJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (rt *Runtime) handleGetTimeline(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = DefaultDebugRoomID
	}
	limit := parseLimit(r, 50, 200)

	rows, err := rt.Store.ListRecentTimeline(r.Context(), roomID, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]APITimelineMessage, 0, len(rows))
	for _, row := range rows {
		var blocks []protocol.ContentBlock
		if row.ContentJSON != "" {
			_ = json.Unmarshal([]byte(row.ContentJSON), &blocks)
		}
		if len(blocks) == 0 && strings.TrimSpace(row.RenderedText) != "" {
			blocks = []protocol.ContentBlock{{Type: protocol.BlockText, Text: row.RenderedText}}
		}
		if blocks == nil {
			blocks = []protocol.ContentBlock{}
		}
		items = append(items, APITimelineMessage{
			MsgID:       row.MsgID,
			ChannelID:   row.ChannelID,
			RoomID:      row.RoomID,
			SenderID:    row.SenderID,
			SenderName:  row.SenderName,
			IsBot:       row.IsBot,
			TimestampMs: row.TimestampMs,
			Blocks:      blocks,
		})
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (rt *Runtime) handleChatSend(w http.ResponseWriter, r *http.Request) {
	var params protocol.ChatSendParams
	if err := decodeJSONBody(r, &params); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rt.HandleChatSend(r.Context(), params); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (rt *Runtime) handleRunTrigger(w http.ResponseWriter, r *http.Request) {
	var params protocol.RunTriggerParams
	if err := decodeJSONBody(r, &params); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rt.HandleRunTrigger(r.Context(), params); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (rt *Runtime) buildAPIRuntimeConfig(ctx context.Context) (APIRuntimeConfig, error) {
	// Strictly use LoadRuntimeConfigView so raw secrets never enter the API layer.
	view, err := rt.Store.LoadRuntimeConfigView(ctx)
	if err != nil {
		return APIRuntimeConfig{}, err
	}

	secretMap := make(map[string]store.SecretStatus, len(view.Secrets))
	for _, s := range view.Secrets {
		secretMap[s.Key] = s
	}

	providers := make([]APIProviderConfig, 0, len(view.Config.Providers))
	for _, p := range view.Config.Providers {
		secKey := p.SecretKey
		if secKey == "" {
			secKey = providerSecretKey(p.ID)
		}
		secStatus := secretMap[secKey]
		providers = append(providers, APIProviderConfig{
			ID:         p.ID,
			Name:       p.Name,
			Kind:       p.Kind,
			BaseURL:    p.BaseURL,
			SecretKey:  secKey,
			HasAPIKey:  secStatus.Configured,
			APIKeyHint: secStatus.MaskedHint,
			Enabled:    p.Enabled,
		})
	}

	modelsByID := make(map[string]store.ModelConfig, len(view.Config.Models))
	for _, m := range view.Config.Models {
		modelsByID[m.ID] = m
	}

	slotEntries := []struct {
		name    string
		modelID string
	}{
		{"chat", view.Config.Slots.Chat},
		{"decision", view.Config.Slots.Decision},
		{"summary", view.Config.Slots.Summary},
		{"vision", view.Config.Slots.Vision},
		{"embedding", view.Config.Slots.Embedding},
		{"tts", view.Config.Slots.TTS},
		{"stt", view.Config.Slots.STT},
	}
	slots := make([]APIModelSlotConfig, 0, len(slotEntries))
	for _, se := range slotEntries {
		if se.modelID == "" {
			continue
		}
		m, ok := modelsByID[se.modelID]
		if !ok {
			continue
		}
		slots = append(slots, APIModelSlotConfig{
			Slot:       se.name,
			ProviderID: m.ProviderID,
			ModelID:    m.ModelID,
			Capabilities: &APIModelSlotCapabilities{
				Vision:  m.Capabilities.Vision,
				AudioIn: m.Capabilities.AudioIn,
				VideoIn: m.Capabilities.VideoIn,
			},
		})
	}

	mcpTools := 0
	if rt.MCP != nil {
		mcpTools = len(rt.MCP.Snapshot().Tools())
	}
	rt.mcpErrsMu.RLock()
	mcpErrCopy := make(map[string]string, len(rt.mcpErrs))
	for k, v := range rt.mcpErrs {
		mcpErrCopy[k] = v
	}
	rt.mcpErrsMu.RUnlock()

	mcpServers := make([]APIMCPServerConfig, 0, len(view.Config.MCPServers))
	for _, s := range view.Config.MCPServers {
		status := "disconnected"
		if s.Enabled {
			if _, hasErr := mcpErrCopy[s.ID]; hasErr {
				status = "error"
			} else {
				status = "connected"
			}
		}
		mcpServers = append(mcpServers, APIMCPServerConfig{
			ID:         s.ID,
			Name:       s.Name,
			Transport:  s.Transport,
			Endpoint:   s.URL,
			Command:    s.Command,
			Args:       s.Args,
			Enabled:    s.Enabled,
			ToolsCount: mcpTools,
			Status:     status,
		})
	}

	return APIRuntimeConfig{
		Providers:  providers,
		ModelSlots: slots,
		MCPServers: mcpServers,
		Features: APIFeaturesConfig{
			MemoryEnabled:      view.Config.Features.MemoryEnabled,
			EmotionEnabled:     view.Config.Features.EmotionEnabled,
			MemeCollectEnabled: view.Config.Features.MemeCollectEnabled,
			BashEnabled:        view.Config.Features.BashToolsEnabled,
		},
	}, nil
}

func (rt *Runtime) buildPersonaResponse(ctx context.Context, roomID string) (APIPersonaResponse, error) {
	core, ok, err := rt.Store.GetPersonaCore(ctx, roomID)
	if err != nil {
		return APIPersonaResponse{}, err
	}
	if !ok && roomID != store.DefaultPersonaRoomID {
		core, ok, err = rt.Store.GetPersonaCore(ctx, store.DefaultPersonaRoomID)
		if err != nil {
			return APIPersonaResponse{}, err
		}
	}
	beliefs := core.Beliefs
	if !ok || strings.TrimSpace(beliefs) == "" {
		beliefs = persona.DefaultBeliefs()
	}
	temp := persona.TemperamentFromStore(core.Temperament)

	rawTraits, err := rt.Store.ListPersonaTraits(ctx)
	if err != nil {
		return APIPersonaResponse{}, err
	}
	traits := make([]APIPersonaTrait, 0, len(rawTraits))
	for _, tr := range rawTraits {
		traits = append(traits, APIPersonaTrait{
			ID:        tr.ID,
			Text:      tr.Content,
			Weight:    1.0,
			UpdatedAt: msToRFC3339(tr.UpdatedAtMs),
		})
	}

	rawSnaps, err := rt.Store.ListPersonaSnapshots(ctx, 25)
	if err != nil {
		return APIPersonaResponse{}, err
	}
	snaps := make([]APIPersonaSnapshot, 0, len(rawSnaps))
	for _, s := range rawSnaps {
		snaps = append(snaps, toAPISnapshot(s))
	}

	return APIPersonaResponse{
		CoreBeliefs: beliefs,
		Traits:      traits,
		Temperament: storeTemperamentToAPI(temp),
		PAD:         rt.currentAPIPAD(ctx, roomID),
		Snapshots:   snaps,
	}, nil
}

func (rt *Runtime) currentAPIPAD(ctx context.Context, roomID string) APIPADState {
	if rt.Emotion != nil {
		if st, err := rt.Emotion.CurrentFeeling(ctx, roomID, rt.clock()); err == nil {
			v := st.State.Value
			return APIPADState{
				Value:     v,
				Feeling:   st.InnerFeeling,
				Intensity: math.Sqrt(v.P*v.P + v.A*v.A + v.D*v.D),
				UpdatedAt: st.State.UpdatedAt.UTC().Format(time.RFC3339),
			}
		}
	}
	def := pad.DefaultTemperament().Baseline
	return APIPADState{
		Value:     def,
		Intensity: def.Norm(),
		UpdatedAt: rt.clock().UTC().Format(time.RFC3339),
	}
}

func toTraceSummary(tr store.RunTrace) APIRunTraceSummary {
	var gateData struct {
		Score *float64 `json:"score"`
	}
	_ = json.Unmarshal([]byte(tr.GateJSON), &gateData)

	var stepsEnv StoredTraceSteps
	_ = json.Unmarshal([]byte(tr.StepsJSON), &stepsEnv)

	var durMs int64
	if tr.FinishedAtMs > tr.StartedAtMs {
		durMs = tr.FinishedAtMs - tr.StartedAtMs
	}
	return APIRunTraceSummary{
		RunID:        tr.RunID,
		RoomID:       tr.RoomID,
		Source:       tr.TriggerSource,
		Status:       tr.Status,
		GateScore:    gateData.Score,
		StepsCount:   len(stepsEnv.Steps),
		DurationMs:   durMs,
		TokensIn:     stepsEnv.Usage.InputTokens,
		TokensOut:    stepsEnv.Usage.OutputTokens,
		CachedTokens: stepsEnv.Usage.CachedTokens,
		StartedAt:    msToRFC3339(tr.StartedAtMs),
		Summary:      stepsEnv.Summary,
	}
}

func toTraceDetail(tr store.RunTrace) APIRunTraceDetail {
	sum := toTraceSummary(tr)
	var padEnv StoredTracePAD
	_ = json.Unmarshal([]byte(tr.PADJSON), &padEnv)

	var stepsEnv StoredTraceSteps
	_ = json.Unmarshal([]byte(tr.StepsJSON), &stepsEnv)
	if stepsEnv.Steps == nil {
		stepsEnv.Steps = []TraceStep{}
	}

	var memHits []TraceMemoryHit
	_ = json.Unmarshal([]byte(tr.MemoriesJSON), &memHits)

	var actions []protocol.OutboundAction
	_ = json.Unmarshal([]byte(tr.ActionsJSON), &actions)

	return APIRunTraceDetail{
		APIRunTraceSummary: sum,
		PADBefore:          padEnv.Before,
		PADAfter:           padEnv.After,
		InnerFeeling:       padEnv.InnerFeeling,
		MemoryHits:         memHits,
		ToolCalls:          stepsEnv.ToolCalls,
		Actions:            actions,
		Steps:              stepsEnv.Steps,
		Error:              tr.ErrorText,
	}
}

func toAPIMemory(m store.Memory, nowMs int64, loc *time.Location) APIMemoryItem {
	ents := m.Entities
	if ents == nil {
		ents = []string{}
	}
	var tEvent string
	if m.TEventMs > 0 {
		tEvent = time.UnixMilli(m.TEventMs).In(loc).Format("2006-01-02")
	}
	return APIMemoryItem{
		ID:           m.ID,
		RoomID:       m.RoomID,
		Content:      m.Content,
		Kind:         mapStoreMemoryKind(m.Kind),
		Entities:     ents,
		Strength:     memory.ComputeStrength(m, nowMs, 0, 0),
		RecallCount:  m.RecallCount,
		TEvent:       tEvent,
		SupersededBy: m.SupersededBy,
		CreatedAt:    msToRFC3339(m.CreatedAtMs),
		UpdatedAt:    msToRFC3339(m.UpdatedAtMs),
	}
}

func mapStoreMemoryKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case store.MemoryKindEpisodic, "episode":
		return "episode"
	case store.MemoryKindHabit, "preference":
		return "preference"
	case "relation":
		return "relation"
	default:
		return "fact"
	}
}

func memoryKindMatches(storeKind, filter string) bool {
	f := strings.ToLower(strings.TrimSpace(filter))
	return strings.EqualFold(storeKind, f) || strings.EqualFold(mapStoreMemoryKind(storeKind), f)
}

func toAPISnapshot(s store.PersonaSnapshot) APIPersonaSnapshot {
	traits := make([]APIPersonaTrait, 0, len(s.Traits))
	for _, tr := range s.Traits {
		traits = append(traits, APIPersonaTrait{
			ID:        tr.ID,
			Text:      tr.Content,
			Weight:    1.0,
			UpdatedAt: msToRFC3339(tr.UpdatedAtMs),
		})
	}
	return APIPersonaSnapshot{
		Version:     s.ID,
		CreatedAt:   msToRFC3339(s.CreatedAtMs),
		Reason:      s.Reason,
		CoreBeliefs: s.Core.Beliefs,
		Traits:      traits,
	}
}

func storeTemperamentToAPI(t pad.Temperament) APIPADTemperament {
	return APIPADTemperament{
		InitialBaseline: t.InitialBaseline,
		Baseline:        t.Baseline,
		Sensitivity:     t.Sensitivity,
		HalfLifePMs:     t.HalfLifeP.Milliseconds(),
		HalfLifeAMs:     t.HalfLifeA.Milliseconds(),
		HalfLifeDMs:     t.HalfLifeD.Milliseconds(),
		DeadzoneRadius:  t.DeadzoneRadius,
	}
}

func apiTemperamentToStore(in APIPADTemperament) store.TemperamentParams {
	def := pad.DefaultTemperament()
	t := pad.Temperament{
		InitialBaseline: in.InitialBaseline.Clamp(),
		Baseline:        in.Baseline.Clamp(),
		Sensitivity:     in.Sensitivity,
		HalfLifeP:       time.Duration(in.HalfLifePMs) * time.Millisecond,
		HalfLifeA:       time.Duration(in.HalfLifeAMs) * time.Millisecond,
		HalfLifeD:       time.Duration(in.HalfLifeDMs) * time.Millisecond,
		DeadzoneRadius:  in.DeadzoneRadius,
	}
	if t.HalfLifeP <= 0 {
		t.HalfLifeP = def.HalfLifeP
	}
	if t.HalfLifeA <= 0 {
		t.HalfLifeA = def.HalfLifeA
	}
	if t.HalfLifeD <= 0 {
		t.HalfLifeD = def.HalfLifeD
	}
	if t.DeadzoneRadius <= 0 {
		t.DeadzoneRadius = def.DeadzoneRadius
	}
	return persona.TemperamentToStore(t)
}

func toAPIMeme(m store.Meme) APIMemeItem {
	return APIMemeItem{
		ID:          m.Hash,
		Hash:        m.Hash,
		BlobID:      m.Hash,
		ThumbURL:    "/api/memes/" + m.Hash + "/thumb",
		ImageURL:    "/api/memes/" + m.Hash + "/image",
		OverlayText: m.OverlayText,
		Description: m.Description,
		EmotionTags: splitTags(m.Emotions),
		SceneTags:   splitTags(m.Scenes),
		UseCount:    m.UseCount,
		Blocked:     m.Status == store.MemeStatusDeleted,
		CreatedAt:   msToRFC3339(m.CreatedAtMs),
	}
}

func splitTags(raw string) []string {
	out := make([]string, 0)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '、' || r == '|'
	}) {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func memeMatchesTag(item APIMemeItem, tag string) bool {
	lower := strings.ToLower(tag)
	for _, t := range item.EmotionTags {
		if strings.Contains(strings.ToLower(t), lower) {
			return true
		}
	}
	for _, t := range item.SceneTags {
		if strings.Contains(strings.ToLower(t), lower) {
			return true
		}
	}
	return false
}

func inferProtocolForSlot(slot, providerKind string) string {
	switch slot {
	case "decision":
		return google.ProtocolDecisions
	case "embedding":
		return openaiembed.Protocol
	case "tts":
		return fishaudio.Protocol
	case "stt", "asr":
		return whisper.Protocol
	default:
		_ = providerKind
		return google.ProtocolInteractions
	}
}

func slugID(name, fallback string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else if sb.Len() > 0 {
			sb.WriteByte('-')
		}
	}
	s := strings.Trim(sb.String(), "-")
	if s == "" {
		return fallback
	}
	return s
}

func msToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func parseLimit(r *http.Request, def, maxVal int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > maxVal {
		return maxVal
	}
	return n
}

func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeAPIJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeAPIJSON(w, status, map[string]string{"error": msg})
}
