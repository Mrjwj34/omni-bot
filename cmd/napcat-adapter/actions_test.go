package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"omni-bot/internal/channel"
	"omni-bot/internal/protocol"
)

type recordedAction struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
	Echo   string         `json:"echo"`
}

func startFakeOneBotWS(
	t *testing.T,
	handler func(req recordedAction) (status string, retcode int, data any, wording string, respond bool),
) (*httptest.Server, *OneBotClient) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()

		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req recordedAction
			if err := json.Unmarshal(raw, &req); err != nil {
				continue
			}
			status, retcode, data, wording, respond := handler(req)
			if !respond {
				continue
			}
			resp := map[string]any{
				"status":  status,
				"retcode": retcode,
				"data":    data,
				"wording": wording,
				"echo":    req.Echo,
			}
			out, _ := json.Marshal(resp)
			if err := ws.Write(r.Context(), websocket.MessageText, out); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial fake onebot ws: %v", err)
	}
	client := NewOneBotClient(t.Context(), ws, OneBotClientOptions{
		ActionTimeout: 2 * time.Second,
		Logger:        slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() {
		client.Close()
		client.Wait()
	})
	return srv, client
}

func TestOneBotClientAllEightActions(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		recorded []recordedAction
	)

	_, client := startFakeOneBotWS(t, func(req recordedAction) (string, int, any, string, bool) {
		mu.Lock()
		recorded = append(recorded, req)
		mu.Unlock()

		switch req.Action {
		case actionSendGroupMsg:
			return "ok", 0, map[string]any{"message_id": 9001}, "", true
		case actionSetMsgEmojiLike:
			return "ok", 0, map[string]any{}, "", true
		case actionGroupPoke:
			return "ok", 0, map[string]any{}, "", true
		case actionGetMsg:
			return "ok", 0, map[string]any{
				"time":         1730000100,
				"message_type": "group",
				"message_id":   9001,
				"group_id":     888888,
				"sender":       map[string]any{"user_id": 1001, "nickname": "Alice", "card": "AliceCard"},
				"message":      []map[string]any{{"type": "text", "data": map[string]any{"text": "hi"}}},
			}, "", true
		case actionGetForwardMsg:
			return "ok", 0, map[string]any{
				"messages": []map[string]any{
					{
						"type": "node",
						"data": map[string]any{
							"user_id":  "1002",
							"nickname": "Bob",
							"content":  []map[string]any{{"type": "text", "data": map[string]any{"text": "nested"}}},
						},
					},
				},
			}, "", true
		case actionGetRecord:
			return "ok", 0, map[string]any{
				"file":      "/tmp/voice.wav",
				"url":       "https://cdn.example/voice.wav",
				"file_name": "voice.wav",
			}, "", true
		case actionGetFile:
			return "ok", 0, map[string]any{
				"file":      "/tmp/video.mp4",
				"file_size": "2048",
			}, "", true
		case actionGetGroupMemberInfo:
			return "ok", 0, map[string]any{
				"group_id": 888888,
				"user_id":  1001,
				"nickname": "Alice",
				"card":     "AliceCard",
				"role":     "member",
			}, "", true
		default:
			return "failed", 1404, nil, "unknown action", true
		}
	})

	ctx := t.Context()

	msgID, err := client.SendGroupMsg(ctx, 888888, []OneBotSegment{{Type: "text", Data: map[string]any{"text": "hello"}}})
	if err != nil || msgID != "9001" {
		t.Fatalf("SendGroupMsg = (%q, %v), want (9001, nil)", msgID, err)
	}
	if err := client.SetMsgEmojiLike(ctx, "9001", "424"); err != nil {
		t.Fatalf("SetMsgEmojiLike: %v", err)
	}
	if err := client.GroupPoke(ctx, 888888, 1001); err != nil {
		t.Fatalf("GroupPoke: %v", err)
	}
	detail, err := client.GetMsg(ctx, "9001")
	if err != nil || string(detail.MessageID) != "9001" || detail.Sender.Card != "AliceCard" {
		t.Fatalf("GetMsg = (%+v, %v)", detail, err)
	}
	fwdNodes, err := client.GetForwardMsg(ctx, "fwd-99")
	if err != nil || len(fwdNodes) != 1 || fwdNodes[0].Data.Nickname != "Bob" {
		t.Fatalf("GetForwardMsg = (%+v, %v)", fwdNodes, err)
	}
	rec, err := client.GetRecord(ctx, "voice.amr", "wav")
	if err != nil || rec.FileName != "voice.wav" {
		t.Fatalf("GetRecord = (%+v, %v)", rec, err)
	}
	fRes, err := client.GetFile(ctx, "file-123")
	if err != nil || string(fRes.FileSize) != "2048" {
		t.Fatalf("GetFile = (%+v, %v)", fRes, err)
	}
	member, err := client.GetGroupMemberInfo(ctx, 888888, 1001, true)
	if err != nil || member.Card != "AliceCard" || member.Role != "member" {
		t.Fatalf("GetGroupMemberInfo = (%+v, %v)", member, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 8 {
		t.Fatalf("recorded %d actions, want 8", len(recorded))
	}
	seenActions := make([]string, len(recorded))
	for i, r := range recorded {
		seenActions[i] = r.Action
		if r.Echo == "" {
			t.Fatalf("action %q missing correlation echo", r.Action)
		}
	}
	wantOrder := []string{
		actionSendGroupMsg,
		actionSetMsgEmojiLike,
		actionGroupPoke,
		actionGetMsg,
		actionGetForwardMsg,
		actionGetRecord,
		actionGetFile,
		actionGetGroupMemberInfo,
	}
	if !reflect.DeepEqual(seenActions, wantOrder) {
		t.Fatalf("actions = %v, want %v", seenActions, wantOrder)
	}
}

func TestActionExecutorExecuteOutbound(t *testing.T) {
	t.Parallel()

	blobStore := newTestMemBlobStore()
	hub := channel.NewHub(channel.Options{Blobs: blobStore, Logger: slog.New(slog.DiscardHandler)})
	kernelSrv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		kernelSrv.Close()
	})

	blobClient := NewHTTPBlobClient(kernelSrv.URL, kernelSrv.Client())
	memeBytes := []byte("\x89PNG\r\n\x1a\nfake-sticker-image")
	memeRef, err := blobClient.Upload(t.Context(), "image/png", memeBytes)
	if err != nil {
		t.Fatalf("upload meme blob: %v", err)
	}
	voiceBytes := []byte("RIFF\x24\x00\x00\x00WAVEtts-audio-bytes")
	voiceRef, err := blobClient.Upload(t.Context(), "audio/wav", voiceBytes)
	if err != nil {
		t.Fatalf("upload voice blob: %v", err)
	}

	var (
		mu       sync.Mutex
		recorded []recordedAction
		msgSeq   = 100
	)
	_, client := startFakeOneBotWS(t, func(req recordedAction) (string, int, any, string, bool) {
		mu.Lock()
		recorded = append(recorded, req)
		msgSeq++
		id := msgSeq
		mu.Unlock()

		if req.Action == actionSendGroupMsg {
			return "ok", 0, map[string]any{"message_id": id}, "", true
		}
		return "ok", 0, map[string]any{}, "", true
	})

	exec := NewActionExecutor(client, blobClient, slog.New(slog.DiscardHandler))
	actions := []protocol.OutboundAction{
		{Type: protocol.ActionSendText, Text: "早安呀", ReplyToMsgID: "42"},
		{Type: protocol.ActionSendMeme, Media: &memeRef},
		{Type: protocol.ActionSendVoice, Media: &voiceRef},
		{Type: protocol.ActionSendVoice, Text: "无音频降级为文字"},
		{Type: protocol.ActionReact, MsgID: "42", Emoji: "424"},
		{Type: protocol.ActionPoke, UserID: "10001"},
	}

	res, err := exec.ExecuteOutbound(t.Context(), "group:888888", actions)
	if err != nil {
		t.Fatalf("ExecuteOutbound: %v", err)
	}
	if res.Delivered != 6 {
		t.Fatalf("Delivered = %d, want 6", res.Delivered)
	}
	if len(res.MsgIDs) != 4 {
		t.Fatalf("MsgIDs = %v, want 4 message IDs", res.MsgIDs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 6 {
		t.Fatalf("recorded %d actions, want 6", len(recorded))
	}

	// Verify meme image payload came from Kernel HTTP blob store as base64:// on OneBot side.
	memeMsg := recorded[1].Params["message"].([]any)
	memeSeg := memeMsg[0].(map[string]any)
	memeFile := memeSeg["data"].(map[string]any)["file"].(string)
	wantMemeFile := "base64://" + base64.StdEncoding.EncodeToString(memeBytes)
	if memeSeg["type"] != "image" || memeFile != wantMemeFile {
		t.Fatalf("unexpected meme segment: %+v", memeSeg)
	}

	// Verify voice record payload.
	voiceMsg := recorded[2].Params["message"].([]any)
	voiceSeg := voiceMsg[0].(map[string]any)
	voiceFile := voiceSeg["data"].(map[string]any)["file"].(string)
	decodedVoice, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(voiceFile, "base64://"))
	if err != nil || !bytes.Equal(decodedVoice, voiceBytes) {
		t.Fatalf("unexpected voice segment payload: err=%v", err)
	}

	// Verify react & poke actions.
	if recorded[4].Action != actionSetMsgEmojiLike || recorded[4].Params["emoji_id"] != "424" {
		t.Fatalf("unexpected react action: %+v", recorded[4])
	}
	if recorded[5].Action != actionGroupPoke || int64(recorded[5].Params["user_id"].(float64)) != 10001 {
		t.Fatalf("unexpected poke action: %+v", recorded[5])
	}
}

func TestOneBotClientErrorsAndTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()

		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var req recordedAction
			_ = json.Unmarshal(raw, &req)
			switch req.Action {
			case actionGroupPoke:
				out, _ := json.Marshal(map[string]any{
					"status":  "failed",
					"retcode": 1400,
					"wording": "user not in group",
					"echo":    req.Echo,
				})
				_ = ws.Write(r.Context(), websocket.MessageText, out)
			case actionGetMsg:
				// Intentionally never reply to trigger timeout.
			}
		}
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(t.Context(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	client := NewOneBotClient(t.Context(), ws, OneBotClientOptions{
		ActionTimeout: 120 * time.Millisecond,
		Logger:        slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() {
		client.Close()
		client.Wait()
	})

	err = client.GroupPoke(t.Context(), 888888, 99999)
	var obErr *OneBotActionError
	if !errors.As(err, &obErr) || obErr.Retcode != 1400 || obErr.Message != "user not in group" {
		t.Fatalf("expected OneBotActionError(1400), got %v", err)
	}

	_, err = client.GetMsg(t.Context(), "123")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}
