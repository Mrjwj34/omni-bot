package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"omni-bot/internal/store"
	"omni-bot/internal/tools"
)

type forecastArgs struct {
	City string `json:"city" jsonschema:"City name to query"`
}

type forecastOut struct {
	City    string `json:"city"`
	TempC   int    `json:"temp_c"`
	Summary string `json:"summary"`
}

type alertArgs struct {
	Region string `json:"region" jsonschema:"Region code"`
}

type alertOut struct {
	Region string `json:"region"`
	Active bool   `json:"active"`
}

type addArgs struct {
	A int `json:"a"`
	B int `json:"b"`
}

type addOut struct {
	Sum int `json:"sum"`
}

type mapSecrets map[string]string

func (m mapSecrets) ResolveSecret(_ context.Context, key string) (string, bool, error) {
	v, ok := m[key]
	return v, ok, nil
}

func TestManagerStreamableHTTPDiscoveryStructuredRefreshAndReload(t *testing.T) {
	var capMu sync.Mutex
	var seenCaps *sdkmcp.ClientCapabilities
	var seenAuth string

	// Server A: weather server with structured output and dynamic tool addition.
	weatherSrv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "weather-srv", Version: "1.0.0"}, nil)
	sdkmcp.AddTool(weatherSrv, &sdkmcp.Tool{
		Name:        "forecast",
		Description: "Return structured weather forecast",
	}, func(_ context.Context, req *sdkmcp.CallToolRequest, in forecastArgs) (*sdkmcp.CallToolResult, forecastOut, error) {
		capMu.Lock()
		if params := req.Session.InitializeParams(); params != nil {
			seenCaps = params.Capabilities
		}
		capMu.Unlock()

		out := forecastOut{
			City:    in.City,
			TempC:   24,
			Summary: "sunny in " + in.City,
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "Forecast for " + in.City + ": 24C sunny"},
			},
		}, out, nil
	})

	weatherHandler := sdkmcp.NewStreamableHTTPHandler(func(_ *http.Request) *sdkmcp.Server {
		return weatherSrv
	}, nil)
	httpWeather := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			capMu.Lock()
			seenAuth = auth
			capMu.Unlock()
		}
		weatherHandler.ServeHTTP(w, r)
	}))
	defer httpWeather.Close()

	// Server B: math server used to verify atomic Reload and in-flight snapshot isolation.
	mathSrv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "math-srv", Version: "1.0.0"}, nil)
	sdkmcp.AddTool(mathSrv, &sdkmcp.Tool{
		Name:        "add",
		Description: "Add two integers",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in addArgs) (*sdkmcp.CallToolResult, addOut, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "sum computed"},
			},
		}, addOut{Sum: in.A + in.B}, nil
	})
	httpMath := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(_ *http.Request) *sdkmcp.Server {
		return mathSrv
	}, nil))
	defer httpMath.Close()

	changedCh := make(chan string, 4)
	mgr := NewManager(ManagerOptions{
		SecretResolver: mapSecrets{"mcp_weather_token": "tok-xyz-123"},
		OnToolsChanged: func(serverID string) {
			select {
			case changedCh <- serverID:
			default:
			}
		},
	})
	defer func() { _ = mgr.Close() }()

	// 1. Connect to weather server (with disabled server skipped cleanly).
	err := mgr.Reload(t.Context(), []store.MCPServerConfig{
		{
			ID:        "srv-1",
			Name:      "weather",
			Transport: "http",
			URL:       httpWeather.URL,
			SecretKey: "mcp_weather_token",
			Enabled:   true,
		},
		{
			ID:        "srv-disabled",
			Name:      "disabled_srv",
			Transport: "http",
			URL:       "http://127.0.0.1:1/unreachable",
			Enabled:   false,
		},
	})
	if err != nil {
		t.Fatalf("Reload weather server: %v", err)
	}

	snap1, releaseSnap1 := mgr.AcquireSnapshot()
	defs1 := snap1.ToolDefs()
	if len(defs1) != 1 || defs1[0].Name != "weather_forecast" {
		t.Fatalf("expected [weather_forecast], got %+v", defs1)
	}

	// 2. Execute prefixed tool via tools.Registry and verify both Text and StructuredContent.
	reg := tools.NewRegistry()
	if err := snap1.RegisterInto(reg); err != nil {
		t.Fatalf("RegisterInto: %v", err)
	}
	res := reg.ExecuteMap(t.Context(), "weather_forecast", map[string]any{"city": "Tokyo"})
	if res.IsError {
		t.Fatalf("weather_forecast failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Forecast for Tokyo: 24C sunny") {
		t.Fatalf("unexpected text result: %q", res.Text)
	}
	structured, ok := res.Structured.(map[string]any)
	if !ok {
		t.Fatalf("expected structuredContent map, got %T (%#v)", res.Structured, res.Structured)
	}
	if structured["city"] != "Tokyo" || structured["temp_c"] != float64(24) {
		t.Fatalf("unexpected structuredContent: %#v", structured)
	}

	// Verify Bearer token and disabled Sampling & Elicitation capabilities on server side.
	capMu.Lock()
	caps := seenCaps
	auth := seenAuth
	capMu.Unlock()
	if auth != "Bearer tok-xyz-123" {
		t.Fatalf("expected Bearer auth header, got %q", auth)
	}
	if caps == nil {
		t.Fatalf("expected InitializeParams capabilities to be captured")
	}
	if caps.Sampling != nil {
		t.Fatalf("expected Sampling capability to be nil (disabled), got %+v", caps.Sampling)
	}
	if caps.Elicitation != nil {
		t.Fatalf("expected Elicitation capability to be nil (disabled), got %+v", caps.Elicitation)
	}

	// 3. Dynamically add a new tool on the live MCP server -> triggers notifications/tools/list_changed.
	sdkmcp.AddTool(weatherSrv, &sdkmcp.Tool{
		Name:        "alerts",
		Description: "Check severe weather alerts",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in alertArgs) (*sdkmcp.CallToolResult, alertOut, error) {
		return nil, alertOut{Region: in.Region, Active: true}, nil
	})

	select {
	case srvID := <-changedCh:
		if srvID != "weather" {
			t.Fatalf("unexpected changed server ID %q", srvID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for notifications/tools/list_changed refresh")
	}

	// Manager snapshot now exposes both weather_alerts and weather_forecast.
	snapAfterChange := mgr.Snapshot()
	if _, ok := snapAfterChange.Get("weather_alerts"); !ok {
		t.Fatalf("expected weather_alerts after list_changed refresh, got %+v", snapAfterChange.ToolDefs())
	}
	// Earlier pinned snapshot remains frozen with only weather_forecast.
	if len(snap1.ToolDefs()) != 1 {
		t.Fatalf("pinned snap1 mutated after list_changed: %+v", snap1.ToolDefs())
	}

	// Verify newly discovered tool executes and preserves structuredContent (and JSON fallback text).
	alertTool, _ := snapAfterChange.Get("weather_alerts")
	alertRes, err := alertTool.Execute(t.Context(), []byte(`{"region":"JP-13"}`))
	if err != nil || alertRes.IsError {
		t.Fatalf("weather_alerts execute failed: res=%+v err=%v", alertRes, err)
	}
	alertStruct, ok := alertRes.Structured.(map[string]any)
	if !ok || alertStruct["region"] != "JP-13" || alertStruct["active"] != true {
		t.Fatalf("unexpected alerts structuredContent: %#v", alertRes.Structured)
	}

	// 4. Reload to math server while snap1 is STILL pinned (simulating an in-flight Run).
	if err := mgr.Reload(t.Context(), []store.MCPServerConfig{
		{
			ID:        "srv-2",
			Name:      "math",
			Transport: "http",
			URL:       httpMath.URL,
			Enabled:   true,
		},
	}); err != nil {
		t.Fatalf("Reload math server: %v", err)
	}

	// New active snapshot has math_add and no weather tools.
	snapMath := mgr.Snapshot()
	if _, ok := snapMath.Get("math_add"); !ok {
		t.Fatalf("expected math_add after Reload, got %+v", snapMath.ToolDefs())
	}
	if _, ok := snapMath.Get("weather_forecast"); ok {
		t.Fatalf("unexpected weather_forecast still present after Reload")
	}

	// In-flight Run holding snap1 can STILL invoke weather_forecast until releaseSnap1()!
	inFlightRes := reg.ExecuteMap(t.Context(), "weather_forecast", map[string]any{"city": "Osaka"})
	if inFlightRes.IsError || !strings.Contains(inFlightRes.Text, "Osaka") {
		t.Fatalf("in-flight pinned snapshot failed during Reload: err=%v text=%q", inFlightRes.IsError, inFlightRes.Text)
	}
	releaseSnap1()

	// Execute math_add on the new snapshot.
	regMath := tools.NewRegistry()
	if err := snapMath.RegisterInto(regMath); err != nil {
		t.Fatalf("RegisterInto math: %v", err)
	}
	mathRes := regMath.ExecuteMap(t.Context(), "math_add", map[string]any{"a": 19, "b": 23})
	if mathRes.IsError {
		t.Fatalf("math_add failed: %s", mathRes.Text)
	}
	mathStruct, ok := mathRes.Structured.(map[string]any)
	if !ok || mathStruct["sum"] != float64(42) {
		t.Fatalf("unexpected math_add structuredContent: %#v", mathRes.Structured)
	}
}

func TestHelperStdioMCPServer(t *testing.T) {
	if os.Getenv("OMNI_TEST_MCP_STDIO_SERVER") != "1" {
		return
	}
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "stdio-helper", Version: "1.0.0"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "ping",
		Description: "Echo pong with tag",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in struct {
		Tag string `json:"tag"`
	}) (*sdkmcp.CallToolResult, map[string]any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "pong:" + in.Tag + ":" + os.Getenv("STDIO_CUSTOM_ENV")},
			},
		}, map[string]any{"pong": in.Tag}, nil
	})
	_ = srv.Run(t.Context(), &sdkmcp.StdioTransport{})
	os.Exit(0)
}

func TestManagerStdioTransport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	mgr := NewManager(ManagerOptions{})
	defer func() { _ = mgr.Close() }()

	if err := mgr.Reload(t.Context(), []store.MCPServerConfig{
		{
			ID:        "stdio-1",
			Name:      "local-cli",
			Transport: "stdio",
			Command:   exe,
			Args:      []string{"-test.run=^TestHelperStdioMCPServer$"},
			Env: map[string]string{
				"OMNI_TEST_MCP_STDIO_SERVER": "1",
				"STDIO_CUSTOM_ENV":           "ok42",
			},
			Enabled: true,
		},
	}); err != nil {
		t.Fatalf("Reload stdio server: %v", err)
	}

	reg := tools.NewRegistry()
	if err := mgr.Snapshot().RegisterInto(reg); err != nil {
		t.Fatalf("RegisterInto stdio: %v", err)
	}
	res := reg.ExecuteMap(t.Context(), "local-cli_ping", map[string]any{"tag": "hello"})
	if res.IsError {
		t.Fatalf("stdio tool failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "pong:hello:ok42") {
		t.Fatalf("unexpected stdio output: %q", res.Text)
	}
	structured, ok := res.Structured.(map[string]any)
	if !ok || structured["pong"] != "hello" {
		t.Fatalf("unexpected stdio structuredContent: %#v", res.Structured)
	}
}
