package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"omni-bot/internal/ai"
	"omni-bot/internal/store"
	"omni-bot/internal/tools"
)

const (
	defaultClientName     = "omni-bot"
	defaultClientVersion  = "1.0.0"
	defaultConnectTimeout = 15 * time.Second
	defaultCallTimeout    = 30 * time.Second
)

// SecretResolver resolves a write-only secret key name into its raw credential for authenticated MCP servers.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, key string) (string, bool, error)
}

// ManagerOptions configures an MCP client Manager.
type ManagerOptions struct {
	ClientName     string
	ClientVersion  string
	ConnectTimeout time.Duration
	CallTimeout    time.Duration
	HTTPClient     *http.Client
	SecretResolver SecretResolver
	Logger         *slog.Logger
	OnToolsChanged func(serverID string)
}

// Snapshot is an immutable view of discovered MCP tools frozen for a single Run.
type Snapshot struct {
	gen      *generation
	tools    []tools.Tool
	byName   map[string]tools.Tool
	released atomic.Bool
}

func emptySnapshot() *Snapshot {
	return &Snapshot{
		byName: map[string]tools.Tool{},
	}
}

func (s *Snapshot) Tools() []tools.Tool {
	if s == nil || len(s.tools) == 0 {
		return nil
	}
	out := make([]tools.Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

func (s *Snapshot) ToolDefs() []ai.ToolDef {
	if s == nil || len(s.tools) == 0 {
		return nil
	}
	defs := make([]ai.ToolDef, len(s.tools))
	for i, t := range s.tools {
		defs[i] = t.Definition()
	}
	return defs
}

func (s *Snapshot) Get(name string) (tools.Tool, bool) {
	if s == nil {
		return nil, false
	}
	t, ok := s.byName[name]
	return t, ok
}

// RegisterInto registers every tool in this frozen snapshot into reg.
func (s *Snapshot) RegisterInto(reg *tools.Registry) error {
	if s == nil || reg == nil {
		return nil
	}
	for _, t := range s.tools {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// Release decrements the reference count when acquired via Manager.AcquireSnapshot.
func (s *Snapshot) Release() {
	if s == nil || s.gen == nil {
		return
	}
	if s.released.CompareAndSwap(false, true) {
		s.gen.release()
	}
}

type generation struct {
	mu       sync.Mutex
	conns    []*serverConn
	refs     int
	retired  bool
	closed   bool
	snapshot atomic.Pointer[Snapshot]
}

func (g *generation) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.refs++
	return true
}

func (g *generation) release() {
	g.mu.Lock()
	g.refs--
	shouldClose := g.retired && g.refs <= 0 && !g.closed
	if shouldClose {
		g.closed = true
	}
	conns := g.conns
	g.mu.Unlock()

	if shouldClose {
		closeConns(conns)
	}
}

func (g *generation) retire() {
	g.mu.Lock()
	g.retired = true
	shouldClose := g.refs <= 0 && !g.closed
	if shouldClose {
		g.closed = true
	}
	conns := g.conns
	g.mu.Unlock()

	if shouldClose {
		closeConns(conns)
	}
}

func (g *generation) forceClose() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.retired = true
	g.closed = true
	conns := g.conns
	g.mu.Unlock()

	closeConns(conns)
}

func (g *generation) addConn(conn *serverConn) {
	g.mu.Lock()
	g.conns = append(g.conns, conn)
	g.mu.Unlock()
}

func (g *generation) rebuildSnapshot() *Snapshot {
	g.mu.Lock()
	conns := append([]*serverConn(nil), g.conns...)
	g.mu.Unlock()

	byName := make(map[string]tools.Tool)
	for _, c := range conns {
		for _, t := range c.currentTools() {
			byName[t.Definition().Name] = t
		}
	}
	names := slices.Sorted(maps.Keys(byName))
	list := make([]tools.Tool, 0, len(names))
	for _, name := range names {
		list = append(list, byName[name])
	}
	snap := &Snapshot{
		gen:    g,
		tools:  list,
		byName: byName,
	}
	g.snapshot.Store(snap)
	return snap
}

func closeConns(conns []*serverConn) {
	for _, c := range conns {
		_ = c.close()
	}
}

// Manager maintains active MCP server connections (Streamable HTTP and stdio) and atomic tool snapshots.
type Manager struct {
	opts ManagerOptions

	reloadMu sync.Mutex
	active   atomic.Pointer[generation]
	closed   atomic.Bool
}

func NewManager(opts ManagerOptions) *Manager {
	if strings.TrimSpace(opts.ClientName) == "" {
		opts.ClientName = defaultClientName
	}
	if strings.TrimSpace(opts.ClientVersion) == "" {
		opts.ClientVersion = defaultClientVersion
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = defaultConnectTimeout
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	m := &Manager{opts: opts}
	initial := &generation{}
	initial.snapshot.Store(emptySnapshot())
	m.active.Store(initial)
	return m
}

// Snapshot returns the current immutable tool snapshot without pinning the underlying generation.
func (m *Manager) Snapshot() *Snapshot {
	gen := m.active.Load()
	if gen == nil {
		return emptySnapshot()
	}
	if snap := gen.snapshot.Load(); snap != nil {
		return snap
	}
	return emptySnapshot()
}

// AcquireSnapshot pins the active generation for an in-flight Run so Reload will not close its sessions until Release() is called.
func (m *Manager) AcquireSnapshot() (*Snapshot, func()) {
	for {
		gen := m.active.Load()
		if gen == nil {
			return emptySnapshot(), func() {}
		}
		if !gen.acquire() {
			if m.closed.Load() {
				return emptySnapshot(), func() {}
			}
			continue
		}
		base := gen.snapshot.Load()
		if base == nil {
			base = emptySnapshot()
		}
		pinned := &Snapshot{
			gen:    gen,
			tools:  base.tools,
			byName: base.byName,
		}
		return pinned, pinned.Release
	}
}

// Reload connects to all enabled MCP servers in configs and atomically swaps the active snapshot.
// Existing in-flight Runs holding an acquired snapshot continue using their frozen connections until released.
func (m *Manager) Reload(ctx context.Context, configs []store.MCPServerConfig) error {
	if m.closed.Load() {
		return errors.New("mcp: manager is closed")
	}
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	newGen := &generation{}
	var errs []error
	usedPrefixes := make(map[string]bool)

	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		prefix := uniqueServerPrefix(cfg, usedPrefixes)
		conn, err := m.connectServer(ctx, newGen, cfg, prefix)
		if err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", serverLabel(cfg), err))
			m.opts.Logger.Warn("failed to connect MCP server",
				slog.String("component", "mcp"),
				slog.String("server", serverLabel(cfg)),
				slog.String("error", err.Error()),
			)
			continue
		}
		newGen.addConn(conn)
	}

	newGen.rebuildSnapshot()
	oldGen := m.active.Swap(newGen)
	if oldGen != nil {
		oldGen.retire()
	}

	return errors.Join(errs...)
}

// Close terminates all active MCP client sessions.
func (m *Manager) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if old := m.active.Swap(nil); old != nil {
		old.forceClose()
	}
	return nil
}

type serverConn struct {
	id          string
	prefix      string
	session     atomic.Pointer[sdkmcp.ClientSession]
	gen         *generation
	mgr         *Manager
	callTimeout time.Duration
	lifeCtx     context.Context
	lifeCancel  context.CancelFunc

	mu        sync.RWMutex
	tools     []tools.Tool
	refreshMu sync.Mutex
	closed    atomic.Bool
}

func (c *serverConn) currentTools() []tools.Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]tools.Tool, len(c.tools))
	copy(out, c.tools)
	return out
}

func (c *serverConn) close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.lifeCancel != nil {
		c.lifeCancel()
	}
	if s := c.session.Load(); s != nil {
		return s.Close()
	}
	return nil
}

func (c *serverConn) refreshTools(ctx context.Context, sess *sdkmcp.ClientSession) error {
	if c.closed.Load() {
		return nil
	}
	if sess == nil {
		sess = c.session.Load()
	}
	if sess == nil {
		return nil
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	if c.closed.Load() {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, c.mgr.opts.ConnectTimeout)
	defer cancel()

	var discovered []tools.Tool
	for t, err := range sess.Tools(listCtx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		if t == nil || strings.TrimSpace(t.Name) == "" {
			continue
		}
		discovered = append(discovered, &mcpTool{
			conn:         c,
			remoteName:   t.Name,
			prefixedName: c.prefix + "_" + t.Name,
			description:  t.Description,
			params:       convertInputSchema(t.InputSchema),
		})
	}

	c.mu.Lock()
	c.tools = discovered
	c.mu.Unlock()
	c.gen.rebuildSnapshot()
	return nil
}

func (m *Manager) connectServer(ctx context.Context, gen *generation, cfg store.MCPServerConfig, prefix string) (*serverConn, error) {
	transport, err := m.buildTransport(ctx, cfg)
	if err != nil {
		return nil, err
	}

	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	conn := &serverConn{
		id:          serverLabel(cfg),
		prefix:      prefix,
		gen:         gen,
		mgr:         m,
		callTimeout: m.opts.CallTimeout,
		lifeCtx:     lifeCtx,
		lifeCancel:  lifeCancel,
	}

	// Explicitly pass an empty ClientCapabilities struct with nil Sampling/Elicitation handlers
	// so autonomous group chat sessions never advertise Sampling or Elicitation to remote servers.
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{
			Name:    m.opts.ClientName,
			Version: m.opts.ClientVersion,
		},
		&sdkmcp.ClientOptions{
			Logger:       m.opts.Logger,
			Capabilities: &sdkmcp.ClientCapabilities{},
			ToolListChangedHandler: func(_ context.Context, req *sdkmcp.ToolListChangedRequest) {
				var sess *sdkmcp.ClientSession
				if req != nil {
					sess = req.Session
				}
				go func() {
					if err := conn.refreshTools(conn.lifeCtx, sess); err != nil {
						if !conn.closed.Load() {
							m.opts.Logger.Warn("failed to refresh MCP tools after list_changed",
								slog.String("component", "mcp"),
								slog.String("server", conn.id),
								slog.String("error", err.Error()),
							)
						}
						return
					}
					if m.opts.OnToolsChanged != nil {
						m.opts.OnToolsChanged(conn.id)
					}
				}()
			},
			ProgressNotificationHandler: func(_ context.Context, req *sdkmcp.ProgressNotificationClientRequest) {
				if req == nil || req.Params == nil {
					return
				}
				m.opts.Logger.Debug("mcp tool progress",
					slog.String("component", "mcp"),
					slog.String("server", conn.id),
					slog.Float64("progress", req.Params.Progress),
					slog.Float64("total", req.Params.Total),
					slog.String("message", req.Params.Message),
				)
			},
		},
	)

	connCtx, cancel := context.WithTimeout(ctx, m.opts.ConnectTimeout)
	defer cancel()

	session, err := client.Connect(connCtx, transport, nil)
	if err != nil {
		lifeCancel()
		return nil, fmt.Errorf("connect: %w", err)
	}
	conn.session.Store(session)

	if err := conn.refreshTools(connCtx, session); err != nil {
		_ = conn.close()
		return nil, err
	}
	return conn, nil
}

func (m *Manager) buildTransport(ctx context.Context, cfg store.MCPServerConfig) (sdkmcp.Transport, error) {
	kind := strings.ToLower(strings.TrimSpace(cfg.Transport))
	if kind == "" {
		if strings.TrimSpace(cfg.URL) != "" {
			kind = "http"
		} else if strings.TrimSpace(cfg.Command) != "" {
			kind = "stdio"
		}
	}

	switch kind {
	case "http", "streamable_http", "streamable-http", "sse":
		endpoint := strings.TrimSpace(cfg.URL)
		if endpoint == "" {
			return nil, errors.New("streamable HTTP transport requires url")
		}
		httpClient := m.opts.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{}
		}
		if key := strings.TrimSpace(cfg.SecretKey); key != "" && m.opts.SecretResolver != nil {
			secret, ok, err := m.opts.SecretResolver.ResolveSecret(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("resolve secret %q: %w", key, err)
			}
			if ok && strings.TrimSpace(secret) != "" {
				baseRT := httpClient.Transport
				if baseRT == nil {
					baseRT = http.DefaultTransport
				}
				cloned := *httpClient
				cloned.Transport = &bearerRoundTripper{
					base:  baseRT,
					token: strings.TrimSpace(secret),
				}
				httpClient = &cloned
			}
		}
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: httpClient,
		}, nil

	case "stdio":
		cmdName := strings.TrimSpace(cfg.Command)
		if cmdName == "" {
			return nil, errors.New("stdio transport requires command")
		}
		cmd := exec.Command(cmdName, cfg.Args...)
		if len(cfg.Env) > 0 {
			env := os.Environ()
			for k, v := range cfg.Env {
				env = append(env, k+"="+v)
			}
			cmd.Env = env
		}
		return &sdkmcp.CommandTransport{
			Command: cmd,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported transport %q", cfg.Transport)
	}
}

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if cloned.Header.Get("Authorization") == "" {
		cloned.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return rt.base.RoundTrip(cloned)
}

type mcpTool struct {
	conn         *serverConn
	remoteName   string
	prefixedName string
	description  string
	params       map[string]any
}

func (t *mcpTool) Definition() ai.ToolDef {
	return ai.ToolDef{
		Name:        t.prefixedName,
		Description: t.description,
		Parameters:  t.params,
	}
}

func (t *mcpTool) Execute(ctx context.Context, argsJSON []byte) (tools.Result, error) {
	if t.conn.closed.Load() {
		return tools.Result{}, fmt.Errorf("mcp tool %q session is closed", t.prefixedName)
	}
	var args map[string]any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return tools.Result{}, fmt.Errorf("decode arguments for %q: %w", t.prefixedName, err)
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && t.conn.callTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, t.conn.callTimeout)
		defer cancel()
	}

	sess := t.conn.session.Load()
	if sess == nil {
		return tools.Result{}, fmt.Errorf("mcp tool %q session is not connected", t.prefixedName)
	}

	res, err := sess.CallTool(callCtx, &sdkmcp.CallToolParams{
		Name:      t.remoteName,
		Arguments: args,
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("mcp call %q: %w", t.prefixedName, err)
	}
	return convertCallToolResult(res), nil
}

func convertCallToolResult(res *sdkmcp.CallToolResult) tools.Result {
	if res == nil {
		return tools.Result{}
	}
	var textParts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *sdkmcp.TextContent:
			if strings.TrimSpace(v.Text) != "" {
				textParts = append(textParts, v.Text)
			}
		case *sdkmcp.ImageContent:
			textParts = append(textParts, fmt.Sprintf("[image: %s, %d bytes]", v.MIMEType, len(v.Data)))
		case *sdkmcp.AudioContent:
			textParts = append(textParts, fmt.Sprintf("[audio: %s, %d bytes]", v.MIMEType, len(v.Data)))
		case *sdkmcp.ResourceLink:
			textParts = append(textParts, fmt.Sprintf("[resource: %s (%s)]", v.Name, v.URI))
		default:
			if raw, err := json.Marshal(c); err == nil {
				textParts = append(textParts, string(raw))
			}
		}
	}

	text := strings.Join(textParts, "\n")
	if text == "" && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			text = string(raw)
		}
	}
	return tools.Result{
		Text:       text,
		Structured: res.StructuredContent,
		IsError:    res.IsError,
	}
}

func convertInputSchema(raw any) map[string]any {
	defaultSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	if raw == nil {
		return defaultSchema
	}
	if m, ok := raw.(map[string]any); ok {
		out := make(map[string]any, len(m))
		maps.Copy(out, m)
		return out
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return defaultSchema
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil || parsed == nil {
		return defaultSchema
	}
	return parsed
}

func serverLabel(cfg store.MCPServerConfig) string {
	if s := strings.TrimSpace(cfg.Name); s != "" {
		return s
	}
	if s := strings.TrimSpace(cfg.ID); s != "" {
		return s
	}
	return "mcp"
}

func uniqueServerPrefix(cfg store.MCPServerConfig, used map[string]bool) string {
	base := sanitizePrefix(serverLabel(cfg))
	if !used[base] {
		used[base] = true
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s_%d", base, i)
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

func sanitizePrefix(raw string) string {
	var sb strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			sb.WriteRune(r)
			prevUnderscore = false
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			sb.WriteByte('_')
			prevUnderscore = true
		}
	}
	clean := strings.Trim(sb.String(), "_-")
	if clean == "" {
		return "mcp"
	}
	return clean
}
