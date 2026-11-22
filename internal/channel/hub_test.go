package channel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/protocol"
)

type memBlob struct {
	meta BlobMeta
	data []byte
}

type memBlobStore struct {
	mu    sync.RWMutex
	blobs map[string]memBlob
}

func newMemBlobStore() *memBlobStore {
	return &memBlobStore{blobs: make(map[string]memBlob)}
}

func (s *memBlobStore) Put(_ context.Context, mimeType string, r io.Reader) (BlobMeta, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return BlobMeta{}, err
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:16])
	meta := BlobMeta{
		BlobID:    id,
		MIMEType:  mimeType,
		SizeBytes: int64(len(data)),
	}
	s.mu.Lock()
	s.blobs[id] = memBlob{meta: meta, data: data}
	s.mu.Unlock()
	return meta, nil
}

func (s *memBlobStore) Open(_ context.Context, blobID string) (io.ReadCloser, BlobMeta, error) {
	s.mu.RLock()
	b, ok := s.blobs[blobID]
	s.mu.RUnlock()
	if !ok {
		return nil, BlobMeta{}, ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(b.data)), b.meta, nil
}

func dialTestWS(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial(%q): %v", wsURL, err)
	}
	t.Cleanup(func() {
		_ = conn.CloseNow()
	})
	return conn
}

func writeEnvelope(t *testing.T, conn *websocket.Conn, env protocol.Envelope) {
	t.Helper()
	frame, err := protocol.EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("ws.Write: %v", err)
	}
}

func readEnvelope(t *testing.T, conn *websocket.Conn) protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws.Read: %v", err)
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	return env
}

func TestHubEndToEndAdapterWebUIAndBlobs(t *testing.T) {
	t.Parallel()

	inboundCh := make(chan protocol.InboundMessage, 4)
	chatSendCh := make(chan protocol.ChatSendParams, 2)
	triggerCh := make(chan protocol.RunTriggerParams, 2)

	blobs := newMemBlobStore()
	hub := NewHub(Options{
		Blobs: blobs,
		OnInbound: func(_ context.Context, msg protocol.InboundMessage) error {
			inboundCh <- msg
			return nil
		},
		OnChatSend: func(_ context.Context, params protocol.ChatSendParams) error {
			chatSendCh <- params
			return nil
		},
		OnRunTrigger: func(_ context.Context, params protocol.RunTriggerParams) error {
			triggerCh <- params
			return nil
		},
	})
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})

	// 1. Connect WebUI observer and subscribe to live topics.
	uiConn := dialTestWS(t, srv.URL)
	subReq, err := protocol.NewRequest("ui-1", protocol.MethodUISubscribe, protocol.UISubscribeParams{
		Topics: []string{
			protocol.EventTraceStep,
			protocol.EventPADUpdate,
			protocol.EventTimelineAppend,
			protocol.EventConfigUpdated,
		},
	})
	if err != nil {
		t.Fatalf("NewRequest(ui.subscribe): %v", err)
	}
	writeEnvelope(t, uiConn, subReq)
	subResp := readEnvelope(t, uiConn)
	var subResult protocol.UISubscribeResult
	if err := subResp.DecodeResult(&subResult); err != nil {
		t.Fatalf("ui.subscribe result: %v", err)
	}
	if len(subResult.Topics) != 4 {
		t.Fatalf("subscribed topics = %v, want 4 topics", subResult.Topics)
	}

	// 2. Connect adapter: inbound before register is rejected with ErrCodeNotRegistered.
	adapterConn := dialTestWS(t, srv.URL)
	unregReq, err := protocol.NewRequest("unreg-1", protocol.MethodChannelInbound, protocol.InboundMessage{
		RoomID: "group-42",
		MsgID:  "m-0",
	})
	if err != nil {
		t.Fatalf("NewRequest(unreg inbound): %v", err)
	}
	writeEnvelope(t, adapterConn, unregReq)
	unregResp := readEnvelope(t, adapterConn)
	if unregResp.Error == nil || unregResp.Error.Code != protocol.ErrCodeNotRegistered {
		t.Fatalf("expected ErrCodeNotRegistered before register, got %+v", unregResp)
	}

	// Register adapter with partial capabilities (no voice, no poke, no multi_bubble).
	regReq, err := protocol.NewRequest("reg-1", protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID:    "qq-main",
		Kind:         "napcat",
		Capabilities: []protocol.Capability{protocol.CapReply, protocol.CapSticker},
	})
	if err != nil {
		t.Fatalf("NewRequest(channel.register): %v", err)
	}
	writeEnvelope(t, adapterConn, regReq)
	regResp := readEnvelope(t, adapterConn)
	var regResult protocol.ChannelRegisterResult
	if err := regResp.DecodeResult(&regResult); err != nil {
		t.Fatalf("channel.register result: %v", err)
	}
	if !regResult.Accepted || regResult.ChannelID != "qq-main" {
		t.Fatalf("unexpected register result: %+v", regResult)
	}
	if !hub.HasAdapter("qq-main") {
		t.Fatalf("expected hub to track registered adapter")
	}
	adapters := hub.Adapters()
	if len(adapters) != 1 || adapters[0].ChannelID != "qq-main" || adapters[0].Kind != "napcat" {
		t.Fatalf("unexpected Adapters(): %+v", adapters)
	}

	// 3. Upload raw blob and reference it in an inbound message (no base64 on WS!).
	client := srv.Client()
	rawPNG := []byte("\x89PNG\r\n\x1a\nfake-png-payload-1234")
	uploadReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/blobs", bytes.NewReader(rawPNG))
	if err != nil {
		t.Fatalf("NewRequest(blob): %v", err)
	}
	uploadReq.Header.Set("Content-Type", "image/png")
	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		t.Fatalf("POST /api/blobs: %v", err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/blobs status = %d, want 201", uploadResp.StatusCode)
	}
	var blobMeta BlobMeta
	if err := json.NewDecoder(uploadResp.Body).Decode(&blobMeta); err != nil {
		t.Fatalf("decode BlobMeta: %v", err)
	}
	if blobMeta.BlobID == "" || blobMeta.MIMEType != "image/png" || blobMeta.SizeBytes != int64(len(rawPNG)) {
		t.Fatalf("unexpected BlobMeta: %+v", blobMeta)
	}

	// Download blob and verify exact bytes and headers.
	getReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/blobs/"+blobMeta.BlobID, nil)
	if err != nil {
		t.Fatalf("NewRequest(GET blob): %v", err)
	}
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("GET /api/blobs: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/blobs status = %d, want 200", getResp.StatusCode)
	}
	if gotCT := getResp.Header.Get("Content-Type"); gotCT != "image/png" {
		t.Fatalf("GET /api/blobs Content-Type = %q, want image/png", gotCT)
	}
	gotBytes, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("read blob body: %v", err)
	}
	if !bytes.Equal(gotBytes, rawPNG) {
		t.Fatalf("downloaded blob mismatch")
	}

	// 4. Send inbound message referencing uploaded blob.
	inboundMsg := protocol.InboundMessage{
		ChannelID:   "qq-main",
		RoomID:      "group-42",
		MsgID:       "m-1",
		SenderID:    "u-100",
		SenderName:  "Alice",
		IsAtBot:     true,
		TimestampMs: 1730000099000,
		Blocks: []protocol.ContentBlock{
			{Type: protocol.BlockText, Text: "看看图"},
			{Type: protocol.BlockImage, Media: &protocol.MediaRef{
				BlobID:    blobMeta.BlobID,
				MIMEType:  blobMeta.MIMEType,
				SizeBytes: blobMeta.SizeBytes,
			}},
		},
	}
	inReq, err := protocol.NewRequest("in-1", protocol.MethodChannelInbound, inboundMsg)
	if err != nil {
		t.Fatalf("NewRequest(inbound): %v", err)
	}
	writeEnvelope(t, adapterConn, inReq)
	inResp := readEnvelope(t, adapterConn)
	var inResult protocol.ChannelInboundResult
	if err := inResp.DecodeResult(&inResult); err != nil {
		t.Fatalf("channel.inbound ack: %v", err)
	}
	if !inResult.Accepted {
		t.Fatalf("expected channel.inbound Accepted=true")
	}

	select {
	case got := <-inboundCh:
		if !reflect.DeepEqual(got, inboundMsg) {
			t.Fatalf("inbound callback mismatch:\ngot:  %+v\nwant: %+v", got, inboundMsg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnInbound")
	}

	// 5. Deliver outbound actions from Hub to adapter; verify capability degradation + RPC ack.
	rawActions, silent := protocol.ParseOutbound("[reply:m-1] 第一句 ||| [poke:u-100] [voice:第二句语音] ||| [meme:#cafebabe]")
	if silent {
		t.Fatalf("expected non-silent parse")
	}

	outErrCh := make(chan error, 1)
	outResCh := make(chan protocol.ChannelOutboundResult, 1)
	go func() {
		res, err := hub.SendOutbound(t.Context(), "qq-main", "group-42", rawActions)
		if err != nil {
			outErrCh <- err
			return
		}
		outResCh <- res
	}()

	outEnv := readEnvelope(t, adapterConn)
	if !outEnv.IsRequest() || outEnv.Method != protocol.MethodChannelOutbound {
		t.Fatalf("expected channel.outbound request, got %+v", outEnv)
	}
	var outParams protocol.ChannelOutboundParams
	if err := outEnv.DecodeParams(&outParams); err != nil {
		t.Fatalf("DecodeParams(outbound): %v", err)
	}
	wantActions := []protocol.OutboundAction{
		{Type: protocol.ActionSendText, Text: "第一句\n第二句语音", ReplyToMsgID: "m-1"},
		{Type: protocol.ActionSendMeme, MemeRef: "cafebabe"},
	}
	if !reflect.DeepEqual(outParams.Actions, wantActions) {
		t.Fatalf("degraded outbound actions =\n%#v\nwant:\n%#v", outParams.Actions, wantActions)
	}

	ackEnv, err := protocol.NewResponse(outEnv.ID, protocol.ChannelOutboundResult{
		Delivered: 2,
		MsgIDs:    []string{"m-2", "m-3"},
	})
	if err != nil {
		t.Fatalf("NewResponse(outbound): %v", err)
	}
	writeEnvelope(t, adapterConn, ackEnv)

	select {
	case err := <-outErrCh:
		t.Fatalf("SendOutbound failed: %v", err)
	case res := <-outResCh:
		if res.Delivered != 2 || !reflect.DeepEqual(res.MsgIDs, []string{"m-2", "m-3"}) {
			t.Fatalf("unexpected SendOutbound result: %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SendOutbound ack")
	}

	// 6. Broadcast WebUI events and verify topic filtering (channel.status was NOT subscribed).
	if err := hub.BroadcastEvent(protocol.EventPADUpdate, "group-42", map[string]float64{"p": 0.4, "a": 0.2, "d": 0.1}); err != nil {
		t.Fatalf("BroadcastEvent(pad.update): %v", err)
	}
	if err := hub.BroadcastEvent(protocol.EventTraceStep, "group-42", map[string]string{"stage": "OnGate"}); err != nil {
		t.Fatalf("BroadcastEvent(trace.step): %v", err)
	}
	ev1 := readEnvelope(t, uiConn)
	ev2 := readEnvelope(t, uiConn)
	if !ev1.IsNotification() || ev1.Method != protocol.MethodUIEvent {
		t.Fatalf("expected ui.event notification, got %+v", ev1)
	}
	var p1, p2 protocol.UIEventParams
	if err := ev1.DecodeParams(&p1); err != nil {
		t.Fatalf("decode ev1: %v", err)
	}
	if err := ev2.DecodeParams(&p2); err != nil {
		t.Fatalf("decode ev2: %v", err)
	}
	if p1.Topic != protocol.EventPADUpdate || p2.Topic != protocol.EventTraceStep {
		t.Fatalf("got topics (%q, %q), want (%q, %q)", p1.Topic, p2.Topic, protocol.EventPADUpdate, protocol.EventTraceStep)
	}

	// 7. WebUI actions (chat.send and run.trigger).
	chatReq, _ := protocol.NewRequest("ui-2", protocol.MethodChatSend, protocol.ChatSendParams{
		ChannelID: "qq-main",
		RoomID:    "group-42",
		Text:      "manual debug message",
	})
	writeEnvelope(t, uiConn, chatReq)
	_ = readEnvelope(t, uiConn)
	select {
	case got := <-chatSendCh:
		if got.Text != "manual debug message" || got.RoomID != "group-42" {
			t.Fatalf("unexpected ChatSendParams: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnChatSend")
	}

	trigReq, _ := protocol.NewRequest("ui-3", protocol.MethodRunTrigger, protocol.RunTriggerParams{
		RoomID:     "group-42",
		Source:     "webui",
		BypassGate: true,
	})
	writeEnvelope(t, uiConn, trigReq)
	_ = readEnvelope(t, uiConn)
	select {
	case got := <-triggerCh:
		if !got.BypassGate || got.RoomID != "group-42" {
			t.Fatalf("unexpected RunTriggerParams: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnRunTrigger")
	}
}

func TestHubReentrantInboundToOutboundNoDeadlock(t *testing.T) {
	t.Parallel()

	var hub *Hub
	hub = NewHub(Options{
		OnInbound: func(ctx context.Context, msg protocol.InboundMessage) error {
			_, err := hub.SendOutbound(ctx, msg.ChannelID, msg.RoomID, []protocol.OutboundAction{
				{Type: protocol.ActionSendText, Text: "echo:" + msg.MsgID},
			})
			return err
		},
	})
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})

	conn := dialTestWS(t, srv.URL)
	regReq, _ := protocol.NewRequest("r1", protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID: "reentrant-ch",
		Kind:      "test",
	})
	writeEnvelope(t, conn, regReq)
	_ = readEnvelope(t, conn)

	inReq, _ := protocol.NewRequest("i1", protocol.MethodChannelInbound, protocol.InboundMessage{
		ChannelID: "reentrant-ch",
		RoomID:    "room-1",
		MsgID:     "m-77",
		SenderID:  "u-1",
		Blocks:    []protocol.ContentBlock{{Type: protocol.BlockText, Text: "ping"}},
	})
	writeEnvelope(t, conn, inReq)

	// Adapter receives the reentrant channel.outbound BEFORE the inbound ack returns.
	outReq := readEnvelope(t, conn)
	if outReq.Method != protocol.MethodChannelOutbound {
		t.Fatalf("expected channel.outbound, got %+v", outReq)
	}
	ack, _ := protocol.NewResponse(outReq.ID, protocol.ChannelOutboundResult{Delivered: 1})
	writeEnvelope(t, conn, ack)

	inAck := readEnvelope(t, conn)
	if inAck.IDKey() != `"i1"` || inAck.Error != nil {
		t.Fatalf("unexpected inbound ack: %+v", inAck)
	}
}

func TestHubSlowSubscriberDoesNotBlockBroadcast(t *testing.T) {
	t.Parallel()

	hub := NewHub(Options{
		SendBuffer: 2,
		Logger:     slog.New(slog.DiscardHandler),
	})
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})

	slowConn := dialTestWS(t, srv.URL)
	sub1, _ := protocol.NewRequest("s1", protocol.MethodUISubscribe, protocol.UISubscribeParams{})
	writeEnvelope(t, slowConn, sub1)
	_ = readEnvelope(t, slowConn)

	fastConn := dialTestWS(t, srv.URL)
	sub2, _ := protocol.NewRequest("s2", protocol.MethodUISubscribe, protocol.UISubscribeParams{})
	writeEnvelope(t, fastConn, sub2)
	_ = readEnvelope(t, fastConn)

	start := time.Now()
	for i := range 20 {
		if err := hub.BroadcastEvent(protocol.EventTimelineAppend, "room-1", map[string]int{"seq": i}); err != nil {
			t.Fatalf("BroadcastEvent(%d): %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("BroadcastEvent took %v, expected non-blocking", elapsed)
	}

	// Healthy client still receives events immediately.
	ev := readEnvelope(t, fastConn)
	if ev.Method != protocol.MethodUIEvent {
		t.Fatalf("expected ui.event on fast client, got %+v", ev)
	}
}

func TestHubOutboundTimeoutAndDisconnect(t *testing.T) {
	t.Parallel()

	hub := NewHub(Options{RPCTimeout: 150 * time.Millisecond})
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})

	conn := dialTestWS(t, srv.URL)
	regReq, _ := protocol.NewRequest("r1", protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID: "silent-adapter",
		Kind:      "test",
	})
	writeEnvelope(t, conn, regReq)
	_ = readEnvelope(t, conn)

	// Case 1: Adapter never responds -> times out cleanly.
	_, err := hub.SendOutbound(t.Context(), "silent-adapter", "room-1", []protocol.OutboundAction{
		{Type: protocol.ActionSendText, Text: "hello"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	// Case 2: Adapter disconnects mid-flight -> caller unblocks immediately.
	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, err := hub.SendOutbound(ctx, "silent-adapter", "room-1", []protocol.OutboundAction{
			{Type: protocol.ActionSendText, Text: "world"},
		})
		errCh <- err
	}()

	// Wait until adapter receives the outbound request, then close connection.
	_ = readEnvelope(t, conn)
	_ = conn.Close(websocket.StatusGoingAway, "bye")

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrChannelClosed) {
			t.Fatalf("expected ErrChannelClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendOutbound did not unblock on disconnect")
	}

	// Disconnect cleanup removes adapter from Hub.
	deadline := time.Now().Add(2 * time.Second)
	for hub.HasAdapter("silent-adapter") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.HasAdapter("silent-adapter") {
		t.Fatal("expected adapter to be cleaned up after disconnect")
	}
}

func TestHubAdapterReconnectReplacesOldConnection(t *testing.T) {
	t.Parallel()

	hub := NewHub(Options{})
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})

	conn1 := dialTestWS(t, srv.URL)
	reg1, _ := protocol.NewRequest("1", protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID:    "qq-main",
		Kind:         "napcat",
		Capabilities: []protocol.Capability{protocol.CapReply},
	})
	writeEnvelope(t, conn1, reg1)
	_ = readEnvelope(t, conn1)

	conn2 := dialTestWS(t, srv.URL)
	reg2, _ := protocol.NewRequest("2", protocol.MethodChannelRegister, protocol.ChannelRegisterParams{
		ChannelID:    "qq-main",
		Kind:         "napcat",
		Capabilities: []protocol.Capability{protocol.CapReply, protocol.CapVoice},
	})
	writeEnvelope(t, conn2, reg2)
	_ = readEnvelope(t, conn2)

	caps, ok := hub.Capabilities("qq-main")
	if !ok || !caps.Has(protocol.CapVoice) {
		t.Fatalf("expected updated capabilities with voice, got %v (ok=%v)", caps, ok)
	}

	// Old connection is closed, and its disconnect cleanup must NOT remove conn2.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, _, err := conn1.Read(ctx); err == nil {
		t.Fatalf("expected superseded connection to close")
	}
	time.Sleep(50 * time.Millisecond)
	if !hub.HasAdapter("qq-main") {
		t.Fatalf("expected reconnect adapter to remain registered")
	}
}

func TestBlobEndpointsMultipartAndErrors(t *testing.T) {
	t.Parallel()

	hub := NewHub(Options{
		Blobs:        newMemBlobStore(),
		MaxBlobBytes: 1024,
	})
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})

	// Multipart upload.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="clip.ogg"`)
	h.Set("Content-Type", "audio/ogg")
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	payload := []byte("OggS-fake-audio-bytes")
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	client := srv.Client()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/blobs", &body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST multipart: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("multipart status = %d, want 201", resp.StatusCode)
	}
	var meta BlobMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.MIMEType != "audio/ogg" || meta.SizeBytes != int64(len(payload)) {
		t.Fatalf("unexpected multipart meta: %+v", meta)
	}

	// Missing blob -> 404.
	getMissing, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/blobs/nonexistent", nil)
	if err != nil {
		t.Fatalf("NewRequest(404): %v", err)
	}
	missResp, err := client.Do(getMissing)
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	_, _ = io.Copy(io.Discard, missResp.Body)
	missResp.Body.Close()
	if missResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing status = %d, want 404", missResp.StatusCode)
	}

	// Oversized raw upload -> 413.
	tooBig := bytes.Repeat([]byte("A"), 2048)
	bigReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/blobs", bytes.NewReader(tooBig))
	if err != nil {
		t.Fatalf("NewRequest(413): %v", err)
	}
	bigReq.Header.Set("Content-Type", "text/plain")
	bigResp, err := client.Do(bigReq)
	if err != nil {
		t.Fatalf("POST tooBig: %v", err)
	}
	_, _ = io.Copy(io.Discard, bigResp.Body)
	bigResp.Body.Close()
	if bigResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST oversized status = %d, want 413", bigResp.StatusCode)
	}
}
