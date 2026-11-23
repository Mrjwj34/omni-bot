package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ConfigKeyProviders = "providers"
	ConfigKeyModels    = "models"
	ConfigKeySlots     = "slots"
	ConfigKeyMCP       = "mcp_servers"
	ConfigKeyFeatures  = "features"
	ConfigKeyBehavior  = "behavior"
)

// ProviderConfig defines a named AI provider endpoint referencing a write-only secret key.
type ProviderConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	BaseURL   string            `json:"base_url"`
	SecretKey string            `json:"secret_key,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	ProxyURL  string            `json:"proxy_url,omitempty"`
	Enabled   bool              `json:"enabled"`
}

// ModelCapabilities declares native multimodal and tool capabilities for a model binding.
type ModelCapabilities struct {
	Vision    bool `json:"vision"`
	AudioIn   bool `json:"audio_in"`
	VideoIn   bool `json:"video_in"`
	ToolCalls bool `json:"tool_calls"`
}

// ModelConfig binds a logical model ID to a provider, protocol, capabilities, and default parameters.
type ModelConfig struct {
	ID            string            `json:"id"`
	ProviderID    string            `json:"provider_id"`
	Protocol      string            `json:"protocol"`
	ModelID       string            `json:"model_id"`
	Capabilities  ModelCapabilities `json:"capabilities"`
	DefaultParams map[string]any    `json:"default_params,omitempty"`
}

// SlotBindings maps runtime functional slots to configured ModelConfig IDs.
type SlotBindings struct {
	Chat      string `json:"chat"`
	Decision  string `json:"decision"`
	Summary   string `json:"summary"`
	Vision    string `json:"vision"`
	Embedding string `json:"embedding"`
	TTS       string `json:"tts"`
	STT       string `json:"stt"`
}

// MCPServerConfig describes an external MCP server using Streamable HTTP or local stdio transport.
type MCPServerConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	URL       string            `json:"url,omitempty"`
	SecretKey string            `json:"secret_key,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Enabled   bool              `json:"enabled"`
}

// FeatureFlags holds hot-reloadable runtime subsystem toggles (bash tools default disabled).
type FeatureFlags struct {
	MemoryEnabled      bool `json:"memory_enabled"`
	EmotionEnabled     bool `json:"emotion_enabled"`
	MemeCollectEnabled bool `json:"meme_collect_enabled"`
	VoiceEnabled       bool `json:"voice_enabled"`
	BashToolsEnabled   bool `json:"bash_tools_enabled"`
}

// BehaviorConfig holds hot-reloadable group chat identity, gate threshold, and debounce settings.
type BehaviorConfig struct {
	BotName         string  `json:"bot_name"`
	BotAliases      string  `json:"bot_aliases"`
	GateThreshold   float64 `json:"gate_threshold"`
	GateAtThreshold float64 `json:"gate_at_threshold"`
	DebounceMs      int     `json:"debounce_ms"`
}

// DefaultBehaviorConfig returns the production-calibrated group chat behavior defaults.
func DefaultBehaviorConfig() BehaviorConfig {
	return BehaviorConfig{
		BotName:         "bk101",
		BotAliases:      "bk101|101",
		GateThreshold:   0.70,
		GateAtThreshold: 0.30,
		DebounceMs:      1400,
	}
}

// RuntimeConfig aggregates typed dynamic configuration stored in dynamic_config.
type RuntimeConfig struct {
	Providers  []ProviderConfig  `json:"providers"`
	Models     []ModelConfig     `json:"models"`
	Slots      SlotBindings      `json:"slots"`
	MCPServers []MCPServerConfig `json:"mcp_servers"`
	Features   FeatureFlags      `json:"features"`
	Behavior   BehaviorConfig    `json:"behavior"`
}

// RuntimeConfigView combines RuntimeConfig with strictly redacted SecretStatus entries for API/UI layers.
type RuntimeConfigView struct {
	Config  RuntimeConfig  `json:"config"`
	Secrets []SecretStatus `json:"secrets"`
}

// GetConfigJSON loads and unmarshals a dynamic_config entry into dst.
func (s *Store) GetConfigJSON(ctx context.Context, key string, dst any) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("store: config key must not be empty")
	}
	var raw string
	err := s.readDB.QueryRowContext(
		ctx,
		`SELECT value_json FROM dynamic_config WHERE key = ?`,
		key,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: get config %q: %w", key, err)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("store: unmarshal config %q: %w", key, err)
	}
	return true, nil
}

// SetConfigJSON marshals val as JSON and upserts it into dynamic_config.
func (s *Store) SetConfigJSON(ctx context.Context, key string, val any) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("store: config key must not be empty")
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("store: marshal config %q: %w", key, err)
	}
	nowMs := time.Now().UnixMilli()
	_, err = s.writeDB.ExecContext(
		ctx,
		`INSERT INTO dynamic_config (key, value_json, updated_at_ms) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
		key, string(raw), nowMs,
	)
	if err != nil {
		return fmt.Errorf("store: set config %q: %w", key, err)
	}
	return nil
}

// ListConfigKeys returns all configured keys in dynamic_config sorted alphabetically.
func (s *Store) ListConfigKeys(ctx context.Context) ([]string, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT key FROM dynamic_config ORDER BY key ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list config keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("store: scan config key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate config keys: %w", err)
	}
	return keys, nil
}

// DefaultFeatureFlags returns safe default runtime feature toggles (Bash tools disabled by default).
func DefaultFeatureFlags() FeatureFlags {
	return FeatureFlags{
		MemoryEnabled:      true,
		EmotionEnabled:     true,
		MemeCollectEnabled: true,
		VoiceEnabled:       false,
		BashToolsEnabled:   false,
	}
}

// LoadRuntimeConfig loads all typed dynamic config sections with sensible defaults for unset keys.
func (s *Store) LoadRuntimeConfig(ctx context.Context) (RuntimeConfig, error) {
	cfg := RuntimeConfig{
		Providers:  []ProviderConfig{},
		Models:     []ModelConfig{},
		MCPServers: []MCPServerConfig{},
		Features:   DefaultFeatureFlags(),
		Behavior:   DefaultBehaviorConfig(),
	}
	if _, err := s.GetConfigJSON(ctx, ConfigKeyProviders, &cfg.Providers); err != nil {
		return RuntimeConfig{}, err
	}
	if _, err := s.GetConfigJSON(ctx, ConfigKeyModels, &cfg.Models); err != nil {
		return RuntimeConfig{}, err
	}
	if _, err := s.GetConfigJSON(ctx, ConfigKeySlots, &cfg.Slots); err != nil {
		return RuntimeConfig{}, err
	}
	if _, err := s.GetConfigJSON(ctx, ConfigKeyMCP, &cfg.MCPServers); err != nil {
		return RuntimeConfig{}, err
	}
	if _, err := s.GetConfigJSON(ctx, ConfigKeyFeatures, &cfg.Features); err != nil {
		return RuntimeConfig{}, err
	}
	if _, err := s.GetConfigJSON(ctx, ConfigKeyBehavior, &cfg.Behavior); err != nil {
		return RuntimeConfig{}, err
	}
	defBeh := DefaultBehaviorConfig()
	if strings.TrimSpace(cfg.Behavior.BotName) == "" {
		cfg.Behavior.BotName = defBeh.BotName
	}
	if cfg.Behavior.GateThreshold <= 0 {
		cfg.Behavior.GateThreshold = defBeh.GateThreshold
	}
	if cfg.Behavior.GateAtThreshold <= 0 {
		cfg.Behavior.GateAtThreshold = defBeh.GateAtThreshold
	}
	if cfg.Behavior.DebounceMs < 0 {
		cfg.Behavior.DebounceMs = defBeh.DebounceMs
	}
	return cfg, nil
}

// SaveRuntimeConfig atomically persists all sections of RuntimeConfig.
func (s *Store) SaveRuntimeConfig(ctx context.Context, cfg RuntimeConfig) error {
	nowMs := time.Now().UnixMilli()
	entries := []struct {
		key string
		val any
	}{
		{ConfigKeyProviders, cfg.Providers},
		{ConfigKeyModels, cfg.Models},
		{ConfigKeySlots, cfg.Slots},
		{ConfigKeyMCP, cfg.MCPServers},
		{ConfigKeyFeatures, cfg.Features},
		{ConfigKeyBehavior, cfg.Behavior},
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin save runtime config: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, e := range entries {
		raw, err := json.Marshal(e.val)
		if err != nil {
			return fmt.Errorf("store: marshal %q: %w", e.key, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO dynamic_config (key, value_json, updated_at_ms) VALUES (?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
			e.key, string(raw), nowMs,
		); err != nil {
			return fmt.Errorf("store: upsert %q: %w", e.key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit runtime config: %w", err)
	}
	return nil
}

// LoadRuntimeConfigView returns dynamic config alongside write-only redacted secret statuses.
func (s *Store) LoadRuntimeConfigView(ctx context.Context) (RuntimeConfigView, error) {
	cfg, err := s.LoadRuntimeConfig(ctx)
	if err != nil {
		return RuntimeConfigView{}, err
	}
	secrets, err := s.ListSecretStatus(ctx)
	if err != nil {
		return RuntimeConfigView{}, err
	}
	if secrets == nil {
		secrets = []SecretStatus{}
	}
	return RuntimeConfigView{
		Config:  cfg,
		Secrets: secrets,
	}, nil
}
