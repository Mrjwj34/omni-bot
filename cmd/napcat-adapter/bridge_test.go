package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/channel"
	"omni-bot/internal/protocol"
)

func TestBridgeEndToEndWithRealHub(t *testing.T) {
	t.Parallel()

	imgBytes := []byte("\x89PNG\r\n\x1a\nend-to-end-inbound-image")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pic.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imgBytes)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(cdn.Close)

	// Real Kernel Channel Hub + HTTP Blob Store.
	inboundCh := make(chan protocol.InboundMessage, 4)
	blobStore := newTestMemBlobStore()
	hub := channel.NewHub(channel.Options{
		Blobs:  blobStore,
		Logger: slog.New(slog.DiscardHandler),
		OnInbound: func(_ context.Context, msg protocol.InboundMessage) error {
			inboundCh <- msg
			return nil
		},
	})
	kernelSrv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		kernelSrv.Close()
	})

	// Fake NapCat OneBot v11 WS server.
	const token = "super-secret-token"
	var (
		mu              sync.Mutex
		outboundActions []recordedAction
		msgSeq          = 20000
	)
	pushEventCh := make(chan []byte, 4)
	napcatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()

		// Goroutine that pushes simulated QQ events to the adapter.
		var writeMu sync.Mutex
		writeFrame := func(payload []byte) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return ws.Write(r.Context(), websocket.MessageText, payload)
		}

		go func() {
			for {
				select {
				case <-r.Context().Done():
					return
				case frame := <-pushEventCh:
					if err := writeFrame(frame); err != nil {
						return
					}
				}
			}
		}()

		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req recordedAction
			if err := json.Unmarshal(raw, &req); err != nil {
				continue
			}

			var respData any
			switch req.Action {
			case actionGetForwardMsg:
				respData = map[string]any{
					"messages": []map[string]any{
						{
							"type": "node",
							"data": map[string]any{
								"user_id":  "30003",
								"nickname": "Charlie",
								"time":     1730000050,
								"content":  []map[string]any{{"type": "text", "data": map[string]any{"text": "合集里的正文"}}},
							},
						},
					},
				}
			case actionSendGroupMsg:
				mu.Lock()
				outboundActions = append(outboundActions, req)
				msgSeq++
				id := msgSeq
				mu.Unlock()
				respData = map[string]any{"message_id": id}
			case actionSetMsgEmojiLike, actionGroupPoke:
				mu.Lock()
				outboundActions = append(outboundActions, req)
				mu.Unlock()
				respData = map[string]any{}
			}

			respBytes, _ := json.Marshal(map[string]any{
				"status":  "ok",
				"retcode": 0,
				"data":    respData,
				"echo":    req.Echo,
			})
			if err := writeFrame(respBytes); err != nil {
				return
			}
		}
	}))
	t.Cleanup(napcatSrv.Close)

	bridge, err := NewBridge(BridgeConfig{
		ChannelID:      "napcat",
		KernelHTTPURL:  kernelSrv.URL,
		NapCatWSURL:    "ws" + strings.TrimPrefix(napcatSrv.URL, "http"),
		NapCatToken:    token,
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		HTTPClient:     cdn.Client(),
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if strings.Contains(bridge.kernelWS, "proto=") {
		t.Fatalf("kernel WS URL must not carry proto query parameter: %q", bridge.kernelWS)
	}

	runCtx, cancelRun := context.WithCancel(t.Context())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- bridge.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancelRun()
		if err := <-runErrCh; err != nil {
			t.Errorf("bridge.Run returned error: %v", err)
		}
	})

	readyCtx, cancelReady := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelReady()
	if err := bridge.WaitReady(readyCtx); err != nil {
		t.Fatalf("bridge.WaitReady: %v", err)
	}

	// 1. Verify full capability registration on real Hub.
	caps, ok := hub.Capabilities("napcat")
	if !ok {
		t.Fatalf("expected napcat adapter registered on hub")
	}
	for _, wantCap := range napcatCapabilities {
		if !caps.Has(wantCap) {
			t.Fatalf("hub missing capability %q (got %v)", wantCap, caps.Slice())
		}
	}

	// 2. Push simulated QQ group message with reply + at bot + text + image + Bilibili card + forward.
	inboundEvent, _ := json.Marshal(OneBotEvent{
		PostType:    "message",
		MessageType: "group",
		SelfID:      99999,
		GroupID:     888888,
		UserID:      20002,
		MessageID:   "10050",
		Time:        1730000500,
		Sender:      OneBotSender{UserID: 20002, Nickname: "Bob", Card: "阿博"},
		Message: json.RawMessage(`[
			{"type":"reply","data":{"id":"10040"}},
			{"type":"at","data":{"qq":"99999","name":"Bot"}},
			{"type":"text","data":{"text":" 帮我看下这个视频和截图"}},
			{"type":"image","data":{"url":"` + cdn.URL + `/pic.png"}},
			{"type":"json","data":{"data":"{\"app\":\"com.tencent.miniapp_01\",\"meta\":{\"detail_1\":{\"title\":\"哔哩哔哩\",\"desc\":\"超燃剪辑\",\"qqdocurl\":\"https://www.bilibili.com/video/BV1GJ411x7h7?share_source=qq\"}}}"}},
			{"type":"forward","data":{"id":"fwd-e2e"}}
		]`),
	})
	pushEventCh <- inboundEvent

	var gotInbound protocol.InboundMessage
	select {
	case gotInbound = <-inboundCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for normalized InboundMessage on Hub")
	}

	if gotInbound.ChannelID != "napcat" || gotInbound.RoomID != "888888" || gotInbound.MsgID != "10050" {
		t.Fatalf("unexpected inbound envelope: %+v", gotInbound)
	}
	if !gotInbound.IsAtBot || gotInbound.ReplyToMsgID != "10040" || gotInbound.SenderName != "阿博" {
		t.Fatalf("unexpected inbound metadata: %+v", gotInbound)
	}
	if len(gotInbound.Blocks) != 6 {
		t.Fatalf("len(Blocks) = %d, want 6: %+v", len(gotInbound.Blocks), gotInbound.Blocks)
	}
	if gotInbound.Blocks[3].Type != protocol.BlockImage || gotInbound.Blocks[3].Media == nil {
		t.Fatalf("block[3] = %+v, want uploaded image", gotInbound.Blocks[3])
	}
	blobClient := NewHTTPBlobClient(kernelSrv.URL, kernelSrv.Client())
	downloadedImg, _, err := blobClient.Download(t.Context(), gotInbound.Blocks[3].Media.BlobID)
	if err != nil || !bytes.Equal(downloadedImg, imgBytes) {
		t.Fatalf("inbound image blob mismatch: err=%v", err)
	}
	if gotInbound.Blocks[4].Type != protocol.BlockLink || gotInbound.Blocks[4].URL != "https://www.bilibili.com/video/BV1GJ411x7h7" {
		t.Fatalf("block[4] = %+v, want clean Bilibili link", gotInbound.Blocks[4])
	}
	if gotInbound.Blocks[5].Type != protocol.BlockForwardTree || len(gotInbound.Blocks[5].ForwardNodes) != 1 {
		t.Fatalf("block[5] = %+v, want expanded forward_tree", gotInbound.Blocks[5])
	}

	// 3. Upload outbound meme & voice blobs to Kernel Hub, then send OutboundActions from Hub -> Bridge -> NapCat.
	memePayload := []byte("\x89PNG\r\n\x1a\noutbound-meme-sticker")
	memeRef, err := blobClient.Upload(t.Context(), "image/png", memePayload)
	if err != nil {
		t.Fatalf("upload meme blob: %v", err)
	}
	voicePayload := []byte("RIFF\x24\x00\x00\x00WAVEoutbound-voice-tts")
	voiceRef, err := blobClient.Upload(t.Context(), "audio/wav", voicePayload)
	if err != nil {
		t.Fatalf("upload voice blob: %v", err)
	}

	outRes, err := hub.SendOutbound(t.Context(), "napcat", "888888", []protocol.OutboundAction{
		{Type: protocol.ActionSendText, Text: "收到了！", ReplyToMsgID: "10050"},
		{Type: protocol.ActionSendMeme, Media: &memeRef},
		{Type: protocol.ActionSendVoice, Media: &voiceRef},
		{Type: protocol.ActionReact, MsgID: "10050", Emoji: "424"},
		{Type: protocol.ActionPoke, UserID: "20002"},
	})
	if err != nil {
		t.Fatalf("hub.SendOutbound: %v", err)
	}
	if outRes.Delivered != 5 {
		t.Fatalf("Delivered = %d, want 5", outRes.Delivered)
	}
	wantMsgIDs := []string{"20001", "20002", "20003"}
	if !reflect.DeepEqual(outRes.MsgIDs, wantMsgIDs) {
		t.Fatalf("MsgIDs = %v, want %v", outRes.MsgIDs, wantMsgIDs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(outboundActions) != 5 {
		t.Fatalf("napcat received %d outbound actions, want 5", len(outboundActions))
	}
	if outboundActions[0].Action != actionSendGroupMsg {
		t.Fatalf("action[0] = %q, want send_group_msg", outboundActions[0].Action)
	}
	// Confirm meme & voice bytes arrived cleanly at OneBot.
	memeSeg := outboundActions[1].Params["message"].([]any)[0].(map[string]any)
	wantMemeB64 := "base64://" + base64.StdEncoding.EncodeToString(memePayload)
	if memeSeg["type"] != "image" || memeSeg["data"].(map[string]any)["file"] != wantMemeB64 {
		t.Fatalf("unexpected outbound meme segment: %+v", memeSeg)
	}
	voiceSeg := outboundActions[2].Params["message"].([]any)[0].(map[string]any)
	wantVoiceB64 := "base64://" + base64.StdEncoding.EncodeToString(voicePayload)
	if voiceSeg["type"] != "record" || voiceSeg["data"].(map[string]any)["file"] != wantVoiceB64 {
		t.Fatalf("unexpected outbound voice segment: %+v", voiceSeg)
	}
	if outboundActions[3].Action != actionSetMsgEmojiLike || outboundActions[3].Params["emoji_id"] != "424" {
		t.Fatalf("action[3] = %+v, want set_msg_emoji_like", outboundActions[3])
	}
	if outboundActions[4].Action != actionGroupPoke || int64(outboundActions[4].Params["user_id"].(float64)) != 20002 {
		t.Fatalf("action[4] = %+v, want group_poke", outboundActions[4])
	}
}

func TestBridgeNapCatReconnect(t *testing.T) {
	t.Parallel()

	hub := channel.NewHub(channel.Options{Logger: slog.New(slog.DiscardHandler)})
	kernelSrv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		kernelSrv.Close()
	})

	var connCount atomic.Int32
	secondReady := make(chan struct{})
	napcatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()

		n := connCount.Add(1)
		if n == 1 {
			// Immediately drop the first connection to verify backoff reconnect.
			_ = ws.Close(websocket.StatusGoingAway, "simulated restart")
			return
		}
		if n == 2 {
			close(secondReady)
		}

		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req recordedAction
			_ = json.Unmarshal(raw, &req)
			resp, _ := json.Marshal(map[string]any{
				"status":  "ok",
				"retcode": 0,
				"data":    map[string]any{"message_id": 777},
				"echo":    req.Echo,
			})
			if err := ws.Write(r.Context(), websocket.MessageText, resp); err != nil {
				return
			}
		}
	}))
	t.Cleanup(napcatSrv.Close)

	bridge, err := NewBridge(BridgeConfig{
		ChannelID:      "napcat-reconn",
		KernelHTTPURL:  kernelSrv.URL,
		NapCatWSURL:    "ws" + strings.TrimPrefix(napcatSrv.URL, "http"),
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     80 * time.Millisecond,
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	runCtx, cancelRun := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- bridge.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancelRun()
		<-done
	})

	select {
	case <-secondReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for NapCat WS reconnect")
	}

	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	if err := bridge.WaitReady(waitCtx); err != nil {
		t.Fatalf("WaitReady after reconnect: %v", err)
	}

	res, err := hub.SendOutbound(t.Context(), "napcat-reconn", "888888", []protocol.OutboundAction{
		{Type: protocol.ActionSendText, Text: "重连后发送成功"},
	})
	if err != nil {
		t.Fatalf("SendOutbound after reconnect: %v", err)
	}
	if res.Delivered != 1 || len(res.MsgIDs) != 1 || res.MsgIDs[0] != "777" {
		t.Fatalf("unexpected result after reconnect: %+v", res)
	}
}

func TestBridgeReverseWSMode(t *testing.T) {
	t.Parallel()

	inboundCh := make(chan protocol.InboundMessage, 2)
	hub := channel.NewHub(channel.Options{
		Logger: slog.New(slog.DiscardHandler),
		OnInbound: func(_ context.Context, msg protocol.InboundMessage) error {
			inboundCh <- msg
			return nil
		},
	})
	kernelSrv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		kernelSrv.Close()
	})

	bridge, err := NewBridge(BridgeConfig{
		ChannelID:      "napcat-rev",
		KernelHTTPURL:  kernelSrv.URL,
		NapCatListen:   "127.0.0.1:0",
		NapCatToken:    "rev-secret",
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     80 * time.Millisecond,
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewBridge reverse: %v", err)
	}

	runCtx, cancelRun := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- bridge.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancelRun()
		<-done
	})

	addrCtx, cancelAddr := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelAddr()
	addr, err := bridge.ReverseAddr(addrCtx)
	if err != nil {
		t.Fatalf("ReverseAddr: %v", err)
	}

	// Unauthorized reverse dial must be rejected with 401.
	_, unauthResp, err := websocket.Dial(t.Context(), "ws://"+addr+"/", nil)
	if err == nil || (unauthResp != nil && unauthResp.StatusCode != http.StatusUnauthorized) {
		t.Fatalf("expected 401 on unauthorized reverse ws, got err=%v resp=%v", err, unauthResp)
	}

	// Connect as simulated NapCat reverse WS client with valid Bearer token.
	revWS, _, err := websocket.Dial(t.Context(), "ws://"+addr+"/", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer rev-secret"}},
	})
	if err != nil {
		t.Fatalf("dial reverse ws: %v", err)
	}
	t.Cleanup(func() { _ = revWS.CloseNow() })

	readyCtx, cancelReady := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelReady()
	if err := bridge.WaitReady(readyCtx); err != nil {
		t.Fatalf("WaitReady reverse: %v", err)
	}

	// Push inbound event over reverse WS.
	evBytes, _ := json.Marshal(OneBotEvent{
		PostType:    "message",
		MessageType: "group",
		GroupID:     666666,
		UserID:      1234,
		MessageID:   "rev-1",
		Time:        1730000900,
		Sender:      OneBotSender{UserID: 1234, Nickname: "RevUser"},
		Message:     json.RawMessage(`[{"type":"text","data":{"text":"reverse hello"}}]`),
	})
	if err := revWS.Write(t.Context(), websocket.MessageText, evBytes); err != nil {
		t.Fatalf("write reverse event: %v", err)
	}

	select {
	case msg := <-inboundCh:
		if msg.RoomID != "666666" || msg.Blocks[0].Text != "reverse hello" {
			t.Fatalf("unexpected reverse inbound msg: %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reverse WS inbound message")
	}
}
