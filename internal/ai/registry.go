package ai

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// KeyRotator cycles through configured API keys in thread-safe round-robin order.
type KeyRotator struct {
	keys []string
	next atomic.Uint64
}

func NewKeyRotator(keys []string) *KeyRotator {
	cleaned := make([]string, 0, len(keys))
	for _, k := range keys {
		if trimmed := strings.TrimSpace(k); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return &KeyRotator{keys: cleaned}
}

func (r *KeyRotator) Len() int {
	if r == nil {
		return 0
	}
	return len(r.keys)
}

func (r *KeyRotator) Next() (string, error) {
	if r == nil || len(r.keys) == 0 {
		return "", &Error{
			Kind:    ErrAuth,
			Message: "no API keys configured",
		}
	}
	idx := (r.next.Add(1) - 1) % uint64(len(r.keys))
	return r.keys[idx], nil
}

// ResolvedModel bundles a resolved model entity with its provider config and key rotator.
type ResolvedModel struct {
	Model    ModelConfig
	Provider ProviderConfig
	Keys     *KeyRotator
}

type (
	LanguageFactory      func(ResolvedModel) (LanguageModel, error)
	DecisionFactory      func(ResolvedModel) (DecisionModel, error)
	EmbeddingFactory     func(ResolvedModel) (EmbeddingModel, error)
	SpeechFactory        func(ResolvedModel) (SpeechModel, error)
	TranscriptionFactory func(ResolvedModel) (TranscriptionModel, error)
)

type providerEntry struct {
	cfg  ProviderConfig
	keys *KeyRotator
}

// Registry resolves providers, model definitions, functional slots, and protocol adapters.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]*providerEntry
	models    map[string]ModelConfig
	slots     map[Slot]string

	langFactories   map[string]LanguageFactory
	decFactories    map[string]DecisionFactory
	embedFactories  map[string]EmbeddingFactory
	speechFactories map[string]SpeechFactory
	asrFactories    map[string]TranscriptionFactory

	langInstances   map[string]LanguageModel
	decInstances    map[string]DecisionModel
	embedInstances  map[string]EmbeddingModel
	speechInstances map[string]SpeechModel
	asrInstances    map[string]TranscriptionModel
}

func NewRegistry() *Registry {
	return &Registry{
		providers:       make(map[string]*providerEntry),
		models:          make(map[string]ModelConfig),
		slots:           make(map[Slot]string),
		langFactories:   make(map[string]LanguageFactory),
		decFactories:    make(map[string]DecisionFactory),
		embedFactories:  make(map[string]EmbeddingFactory),
		speechFactories: make(map[string]SpeechFactory),
		asrFactories:    make(map[string]TranscriptionFactory),
		langInstances:   make(map[string]LanguageModel),
		decInstances:    make(map[string]DecisionModel),
		embedInstances:  make(map[string]EmbeddingModel),
		speechInstances: make(map[string]SpeechModel),
		asrInstances:    make(map[string]TranscriptionModel),
	}
}

func (r *Registry) RegisterProvider(cfg ProviderConfig) error {
	if strings.TrimSpace(cfg.ID) == "" {
		return &Error{Kind: ErrInvalidArg, Message: "provider id is required"}
	}
	copied := cloneProviderConfig(cfg)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[copied.ID] = &providerEntry{
		cfg:  copied,
		keys: NewKeyRotator(copied.APIKeys),
	}
	return nil
}

func (r *Registry) RegisterModel(cfg ModelConfig) error {
	if strings.TrimSpace(cfg.ID) == "" {
		return &Error{Kind: ErrInvalidArg, Message: "model id is required"}
	}
	if strings.TrimSpace(cfg.ProviderID) == "" {
		return &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("model %q requires provider_id", cfg.ID)}
	}
	if strings.TrimSpace(cfg.Protocol) == "" {
		return &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("model %q requires protocol", cfg.ID)}
	}
	copied := cloneModelConfig(cfg)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[copied.ID] = copied
	// Invalidate cached instances for this model ID when its definition is updated.
	delete(r.langInstances, copied.ID)
	delete(r.decInstances, copied.ID)
	delete(r.embedInstances, copied.ID)
	delete(r.speechInstances, copied.ID)
	delete(r.asrInstances, copied.ID)
	return nil
}

func (r *Registry) BindSlot(slot Slot, modelID string) error {
	if strings.TrimSpace(string(slot)) == "" {
		return &Error{Kind: ErrInvalidArg, Message: "slot name is required"}
	}
	if strings.TrimSpace(modelID) == "" {
		return &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("slot %q requires model_id", slot)}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slots[slot] = modelID
	return nil
}

func (r *Registry) RegisterLanguageFactory(protocol string, f LanguageFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.langFactories[protocol] = f
}

func (r *Registry) RegisterDecisionFactory(protocol string, f DecisionFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decFactories[protocol] = f
}

func (r *Registry) RegisterEmbeddingFactory(protocol string, f EmbeddingFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.embedFactories[protocol] = f
}

func (r *Registry) RegisterSpeechFactory(protocol string, f SpeechFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speechFactories[protocol] = f
}

func (r *Registry) RegisterTranscriptionFactory(protocol string, f TranscriptionFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asrFactories[protocol] = f
}

func (r *Registry) SetLanguageModel(modelID string, m LanguageModel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.langInstances[modelID] = m
}

func (r *Registry) SetDecisionModel(modelID string, m DecisionModel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decInstances[modelID] = m
}

func (r *Registry) SetEmbeddingModel(modelID string, m EmbeddingModel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.embedInstances[modelID] = m
}

func (r *Registry) SetSpeechModel(modelID string, m SpeechModel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speechInstances[modelID] = m
}

func (r *Registry) SetTranscriptionModel(modelID string, m TranscriptionModel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asrInstances[modelID] = m
}

func (r *Registry) ResolveSlot(slot Slot) (ResolvedModel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	modelID, ok := r.slots[slot]
	if !ok {
		return ResolvedModel{}, &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("slot %q is not bound", slot)}
	}
	return r.resolveModelLocked(modelID)
}

func (r *Registry) ResolveModel(modelID string) (ResolvedModel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolveModelLocked(modelID)
}

func (r *Registry) NextAPIKey(providerID string) (string, error) {
	r.mu.RLock()
	p, ok := r.providers[providerID]
	r.mu.RUnlock()
	if !ok {
		return "", &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("provider %q not found", providerID)}
	}
	key, err := p.keys.Next()
	if err != nil {
		return "", &Error{Kind: ErrAuth, Provider: providerID, Message: "no API keys configured", Err: err}
	}
	return key, nil
}

func (r *Registry) Language(slot Slot) (LanguageModel, error) {
	return resolveTyped(r, slot, r.langInstances, r.langFactories, "language")
}

func (r *Registry) Decision(slot Slot) (DecisionModel, error) {
	return resolveTyped(r, slot, r.decInstances, r.decFactories, "decision")
}

func (r *Registry) Embedding(slot Slot) (EmbeddingModel, error) {
	return resolveTyped(r, slot, r.embedInstances, r.embedFactories, "embedding")
}

func (r *Registry) Speech(slot Slot) (SpeechModel, error) {
	return resolveTyped(r, slot, r.speechInstances, r.speechFactories, "speech")
}

func (r *Registry) Transcription(slot Slot) (TranscriptionModel, error) {
	return resolveTyped(r, slot, r.asrInstances, r.asrFactories, "transcription")
}

func resolveTyped[T any, F ~func(ResolvedModel) (T, error)](
	r *Registry,
	slot Slot,
	instances map[string]T,
	factories map[string]F,
	kind string,
) (T, error) {
	var zero T

	r.mu.RLock()
	modelID, ok := r.slots[slot]
	if !ok {
		r.mu.RUnlock()
		return zero, &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("slot %q is not bound", slot)}
	}
	if existing, found := instances[modelID]; found {
		r.mu.RUnlock()
		return existing, nil
	}
	resolved, err := r.resolveModelLocked(modelID)
	if err != nil {
		r.mu.RUnlock()
		return zero, err
	}
	factory, ok := factories[resolved.Model.Protocol]
	r.mu.RUnlock()
	if !ok {
		return zero, &Error{
			Kind:    ErrInvalidArg,
			Message: fmt.Sprintf("no %s factory registered for protocol %q", kind, resolved.Model.Protocol),
		}
	}

	// Build outside the lock so factories performing client setup never block other readers.
	built, err := factory(resolved)
	if err != nil {
		return zero, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, found := instances[modelID]; found {
		return existing, nil
	}
	instances[modelID] = built
	return built, nil
}

func (r *Registry) resolveModelLocked(modelID string) (ResolvedModel, error) {
	m, ok := r.models[modelID]
	if !ok {
		return ResolvedModel{}, &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf("model %q not found", modelID)}
	}
	p, ok := r.providers[m.ProviderID]
	if !ok {
		return ResolvedModel{}, &Error{
			Kind:    ErrInvalidArg,
			Message: fmt.Sprintf("provider %q for model %q not found", m.ProviderID, modelID),
		}
	}
	return ResolvedModel{
		Model:    cloneModelConfig(m),
		Provider: cloneProviderConfig(p.cfg),
		Keys:     p.keys,
	}, nil
}

func cloneProviderConfig(cfg ProviderConfig) ProviderConfig {
	out := cfg
	out.APIKeys = slices.Clone(cfg.APIKeys)
	out.Headers = maps.Clone(cfg.Headers)
	return out
}

func cloneModelConfig(cfg ModelConfig) ModelConfig {
	out := cfg
	out.DefaultParams = maps.Clone(cfg.DefaultParams)
	return out
}
