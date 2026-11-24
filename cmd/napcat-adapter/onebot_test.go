package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"omni-bot/internal/channel"
	"omni-bot/internal/protocol"
)

type testMemBlob struct {
	meta channel.BlobMeta
	data []byte
}

type testMemBlobStore struct {
	mu    sync.RWMutex
	blobs map[string]testMemBlob
}

func newTestMemBlobStore() *testMemBlobStore {
	return &testMemBlobStore{blobs: make(map[string]testMemBlob)}
}

func (s *testMemBlobStore) Put(_ context.Context, mimeType string, r io.Reader) (channel.BlobMeta, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return channel.BlobMeta{}, err
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:16])
	meta := channel.BlobMeta{
		BlobID:    id,
		MIMEType:  mimeType,
		SizeBytes: int64(len(data)),
	}
	s.mu.Lock()
	s.blobs[id] = testMemBlob{meta: meta, data: data}
	s.mu.Unlock()
	return meta, nil
}

func (s *testMemBlobStore) Open(_ context.Context, blobID string) (io.ReadCloser, channel.BlobMeta, error) {
	s.mu.RLock()
	b, ok := s.blobs[blobID]
	s.mu.RUnlock()
	if !ok {
		return nil, channel.BlobMeta{}, channel.ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(b.data)), b.meta, nil
}

type stubForwardFetcher struct {
	mu    sync.Mutex
	calls []string
	nodes map[string][]OneBotForwardNode
}

func (f *stubForwardFetcher) GetForwardMsg(_ context.Context, forwardID string) ([]OneBotForwardNode, error) {
	f.mu.Lock()
	f.calls = append(f.calls, forwardID)
	nodes := f.nodes[forwardID]
	f.mu.Unlock()
	return nodes, nil
}

func TestNormalizeGroupMessageSegments(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\nfake-png-image-bytes")
	silkBytes := append([]byte{0x02}, []byte("#!SILK_V3fake-silk-voice-payload")...)
	wavBytes := []byte("RIFF\x24\x00\x00\x00WAVEfmt-transcoded-pcm")
	mp4Bytes := []byte("\x00\x00\x00\x18ftypmp42fake-video-bytes")

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		case "/voice.silk":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(silkBytes)
		case "/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4Bytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cdn.Close)

	blobStore := newTestMemBlobStore()
	hub := channel.NewHub(channel.Options{Blobs: blobStore, Logger: slog.New(slog.DiscardHandler)})
	kernelSrv := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		hub.Close()
		kernelSrv.Close()
	})

	transcodeCalled := 0
	forwards := &stubForwardFetcher{
		nodes: map[string][]OneBotForwardNode{
			"fwd-1": {
				{
					Type: "node",
					Data: &OneBotNodeData{
						UserID:   "2001",
						Nickname: "Bob",
						Time:     1730000010,
						Content:  json.RawMessage(`[{"type":"text","data":{"text":"转发里的消息"}}]`),
					},
				},
			},
		},
	}

	blobClient := NewHTTPBlobClient(kernelSrv.URL, kernelSrv.Client())
	norm := NewNormalizer(NormalizerOptions{
		ChannelID:  "qq-main",
		Blobs:      blobClient,
		Forwards:   forwards,
		HTTPClient: cdn.Client(),
		Logger:     slog.New(slog.DiscardHandler),
		AudioTranscoder: func(_ context.Context, data []byte, srcMIME string) ([]byte, string, error) {
			if needsAudioTranscode(data, srcMIME) {
				transcodeCalled++
				return wavBytes, "audio/wav", nil
			}
			return data, srcMIME, nil
		},
	})

	tests := []struct {
		name      string
		event     OneBotEvent
		wantOK    bool
		checkFunc func(t *testing.T, msg protocol.InboundMessage)
	}{
		{
			name: "ignores non-group private message",
			event: OneBotEvent{
				PostType:    "message",
				MessageType: "private",
				UserID:      1001,
				Message:     json.RawMessage(`[{"type":"text","data":{"text":"hi"}}]`),
			},
			wantOK: false,
		},
		{
			name: "ignores meta heartbeat event",
			event: OneBotEvent{
				PostType: "meta_event",
			},
			wantOK: false,
		},
		{
			name: "parses text at reply image record video forward and json card",
			event: OneBotEvent{
				PostType:    "message",
				MessageType: "group",
				SelfID:      99999,
				GroupID:     888888,
				UserID:      10001,
				MessageID:   "556677",
				Time:        1730000100,
				Sender:      OneBotSender{UserID: 10001, Nickname: "AliceNick", Card: "AliceCard"},
				Message: json.RawMessage(`[
					{"type":"reply","data":{"id":"443322"}},
					{"type":"at","data":{"qq":"99999","name":"Bot"}},
					{"type":"text","data":{"text":" 看看这个"}},
					{"type":"image","data":{"url":"` + cdn.URL + `/img.png","summary":"[表情包]","subType":1}},
					{"type":"record","data":{"url":"` + cdn.URL + `/voice.silk"}},
					{"type":"video","data":{"url":"` + cdn.URL + `/clip.mp4"}},
					{"type":"forward","data":{"id":"fwd-1"}},
					{"type":"json","data":{"data":"{\"app\":\"com.tencent.miniapp_01\",\"meta\":{\"detail_1\":{\"title\":\"哔哩哔哩\",\"desc\":\"手搓内核\",\"qqdocurl\":\"https://www.bilibili.com/video/BV1GJ411x7h7?share_source=qq&p=2\"}}}"}}
				]`),
			},
			wantOK: true,
			checkFunc: func(t *testing.T, msg protocol.InboundMessage) {
				if msg.ChannelID != "qq-main" || msg.RoomID != "888888" || msg.MsgID != "556677" {
					t.Fatalf("unexpected envelope metadata: %+v", msg)
				}
				if msg.SenderName != "AliceCard" || msg.SenderID != "10001" {
					t.Fatalf("expected group card precedence, got sender=%q id=%q", msg.SenderName, msg.SenderID)
				}
				if !msg.IsAtBot || msg.IsBot || msg.ReplyToMsgID != "443322" {
					t.Fatalf("unexpected flags: isAtBot=%v isBot=%v reply=%q", msg.IsAtBot, msg.IsBot, msg.ReplyToMsgID)
				}
				if msg.TimestampMs != 1730000100000 {
					t.Fatalf("timestamp_ms = %d, want 1730000100000", msg.TimestampMs)
				}
				if len(msg.Blocks) != 8 {
					t.Fatalf("got %d blocks, want 8: %+v", len(msg.Blocks), msg.Blocks)
				}

				if msg.Blocks[0].Type != protocol.BlockReplyRef || msg.Blocks[0].ReplyToMsgID != "443322" {
					t.Fatalf("block[0] = %+v, want reply_ref", msg.Blocks[0])
				}
				if msg.Blocks[1].Type != protocol.BlockMention || msg.Blocks[1].TargetID != "99999" {
					t.Fatalf("block[1] = %+v, want mention", msg.Blocks[1])
				}
				if msg.Blocks[2].Type != protocol.BlockText || msg.Blocks[2].Text != " 看看这个" {
					t.Fatalf("block[2] = %+v, want text", msg.Blocks[2])
				}

				imgBlock := msg.Blocks[3]
				if imgBlock.Type != protocol.BlockImage || imgBlock.Media == nil || imgBlock.Media.BlobID == "" {
					t.Fatalf("block[3] = %+v, want uploaded image MediaRef", imgBlock)
				}
				gotImg, gotCT, err := blobClient.Download(t.Context(), imgBlock.Media.BlobID)
				if err != nil || !bytes.Equal(gotImg, pngBytes) || gotCT != "image/png" {
					t.Fatalf("downloaded image mismatch: ct=%q err=%v", gotCT, err)
				}

				audBlock := msg.Blocks[4]
				if audBlock.Type != protocol.BlockAudio || audBlock.Media == nil || audBlock.Media.MIMEType != "audio/wav" {
					t.Fatalf("block[4] = %+v, want transcoded audio/wav MediaRef", audBlock)
				}
				gotAud, _, err := blobClient.Download(t.Context(), audBlock.Media.BlobID)
				if err != nil || !bytes.Equal(gotAud, wavBytes) {
					t.Fatalf("downloaded audio mismatch: err=%v", err)
				}
				if transcodeCalled != 1 {
					t.Fatalf("transcodeCalled = %d, want 1", transcodeCalled)
				}

				vidBlock := msg.Blocks[5]
				if vidBlock.Type != protocol.BlockVideo || vidBlock.Media == nil || vidBlock.Media.BlobID == "" {
					t.Fatalf("block[5] = %+v, want uploaded video MediaRef", vidBlock)
				}

				fwdBlock := msg.Blocks[6]
				if fwdBlock.Type != protocol.BlockForwardTree || len(fwdBlock.ForwardNodes) != 1 {
					t.Fatalf("block[6] = %+v, want forward_tree with 1 node", fwdBlock)
				}
				if fwdBlock.ForwardNodes[0].SenderName != "Bob" || fwdBlock.ForwardNodes[0].Blocks[0].Text != "转发里的消息" {
					t.Fatalf("unexpected forward node: %+v", fwdBlock.ForwardNodes[0])
				}

				linkBlock := msg.Blocks[7]
				if linkBlock.Type != protocol.BlockLink || linkBlock.URL != "https://www.bilibili.com/video/BV1GJ411x7h7?p=2" {
					t.Fatalf("block[7] = %+v, want normalized Bilibili link", linkBlock)
				}

				// Verify serialized InboundMessage never leaks base64 media payloads onto WS frames.
				wireJSON, err := json.Marshal(msg)
				if err != nil {
					t.Fatalf("marshal InboundMessage: %v", err)
				}
				if strings.Contains(string(wireJSON), "base64://") || strings.Contains(string(wireJSON), "fake-png") {
					t.Fatalf("wire InboundMessage leaked raw media payload: %s", wireJSON)
				}
			},
		},
		{
			name: "marks bot self message and falls back to nickname",
			event: OneBotEvent{
				PostType:    "message",
				MessageType: "group",
				SelfID:      99999,
				GroupID:     888888,
				UserID:      99999,
				MessageID:   "778899",
				Time:        1730000200,
				Sender:      OneBotSender{UserID: 99999, Nickname: "OmniBot"},
				Message:     json.RawMessage(`[{"type":"text","data":{"text":"我自己发的"}}]`),
			},
			wantOK: true,
			checkFunc: func(t *testing.T, msg protocol.InboundMessage) {
				if !msg.IsBot || msg.SenderName != "OmniBot" {
					t.Fatalf("expected IsBot=true and SenderName=OmniBot, got %+v", msg)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := norm.NormalizeGroupMessage(t.Context(), tc.event)
			if err != nil {
				t.Fatalf("NormalizeGroupMessage: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.checkFunc != nil {
				tc.checkFunc(t, got)
			}
		})
	}
}

func TestUnpackJSONCard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		wantType protocol.BlockType
		wantURL  string
		wantText string
	}{
		{
			name: "bilibili miniapp card with BV ID and tracking params",
			raw: `{
				"app": "com.tencent.miniapp_01",
				"desc": "哔哩哔哩",
				"prompt": "[QQ小程序]哔哩哔哩",
				"meta": {
					"detail_1": {
						"title": "哔哩哔哩",
						"desc": "【硬核】从零实现内存数据库",
						"qqdocurl": "https://www.bilibili.com/video/BV1xx411c7mD?share_source=qq&vd_source=abc&spm_id_from=333",
						"url": "m.q.qq.com/a/s/ignored"
					}
				}
			}`,
			wantType: protocol.BlockLink,
			wantURL:  "https://www.bilibili.com/video/BV1xx411c7mD",
			wantText: "[哔哩哔哩] 【硬核】从零实现内存数据库 BV1xx411c7mD",
		},
		{
			name: "bilibili b23 short link strips tracking query params",
			raw: `{
				"app": "com.tencent.miniapp_01",
				"meta": {
					"detail_1": {
						"title": "哔哩哔哩",
						"desc": "周末Vlog",
						"qqdocurl": "https://b23.tv/xyz987?share_source=qq&timestamp=1730000000&unique_k=123"
					}
				}
			}`,
			wantType: protocol.BlockLink,
			wantURL:  "https://b23.tv/xyz987",
			wantText: "[哔哩哔哩] 周末Vlog",
		},
		{
			name: "structured news share card",
			raw: `{
				"app": "com.tencent.structmsg",
				"meta": {
					"news": {
						"tag": "知乎",
						"title": "Go 1.26 新特性一览",
						"desc": "性能与标准库更新",
						"jumpUrl": "https://zhuanlan.zhihu.com/p/12345"
					}
				}
			}`,
			wantType: protocol.BlockLink,
			wantURL:  "https://zhuanlan.zhihu.com/p/12345",
			wantText: "[知乎] Go 1.26 新特性一览 性能与标准库更新",
		},
		{
			name: "music share card",
			raw: `{
				"app": "com.tencent.structmsg",
				"meta": {
					"music": {
						"tag": "网易云音乐",
						"title": "夜曲",
						"desc": "周杰伦",
						"jumpUrl": "https://music.163.com/song?id=185809"
					}
				}
			}`,
			wantType: protocol.BlockLink,
			wantURL:  "https://music.163.com/song?id=185809",
			wantText: "[网易云音乐] 夜曲 周杰伦",
		},
		{
			name:     "card without url degrades to clean text",
			raw:      `{"app":"com.tencent.notice","prompt":"群公告：今晚八点开黑"}`,
			wantType: protocol.BlockText,
			wantText: "群公告：今晚八点开黑",
		},
		{
			name:     "invalid json degrades to raw text",
			raw:      `{not valid json`,
			wantType: protocol.BlockText,
			wantText: `{not valid json`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := unpackJSONCard(tc.raw)
			if len(blocks) != 1 {
				t.Fatalf("len(blocks) = %d, want 1 (%+v)", len(blocks), blocks)
			}
			got := blocks[0]
			if got.Type != tc.wantType {
				t.Fatalf("Type = %q, want %q", got.Type, tc.wantType)
			}
			if got.URL != tc.wantURL {
				t.Fatalf("URL = %q, want %q", got.URL, tc.wantURL)
			}
			if got.Text != tc.wantText {
				t.Fatalf("Text = %q, want %q", got.Text, tc.wantText)
			}
		})
	}
}

func TestRecursiveForwardExpansionAndDepthBound(t *testing.T) {
	t.Parallel()

	// Build a 4-level cyclic forward tree (fwd-L1 -> fwd-L2 -> fwd-L3 -> fwd-L1)
	// and verify MaxForwardDepth=2 expands levels 1 & 2, then stops cleanly at depth 2.
	fetcher := &stubForwardFetcher{
		nodes: map[string][]OneBotForwardNode{
			"fwd-L1": {
				{
					Sender:  &OneBotSender{UserID: 101, Card: "L1User"},
					Time:    1730000001,
					Message: json.RawMessage(`[{"type":"text","data":{"text":"level-1"}},{"type":"forward","data":{"id":"fwd-L2"}}]`),
				},
			},
			"fwd-L2": {
				{
					Type: "node",
					Data: &OneBotNodeData{
						Uin:     "102",
						Name:    "L2User",
						Time:    1730000002,
						Content: json.RawMessage(`[{"type":"text","data":{"text":"level-2"}},{"type":"forward","data":{"id":"fwd-L3"}}]`),
					},
				},
			},
			"fwd-L3": {
				{
					Type: "node",
					Data: &OneBotNodeData{
						UserID:   "103",
						Nickname: "L3User",
						Time:     1730000003,
						Content:  json.RawMessage(`[{"type":"forward","data":{"id":"fwd-L1"}}]`),
					},
				},
			},
		},
	}

	norm := NewNormalizer(NormalizerOptions{
		Forwards:        fetcher,
		MaxForwardDepth: 2,
		Logger:          slog.New(slog.DiscardHandler),
	})

	msg, ok, err := norm.NormalizeGroupMessage(t.Context(), OneBotEvent{
		PostType:    "message",
		MessageType: "group",
		GroupID:     12345,
		UserID:      100,
		MessageID:   "m-fwd",
		Time:        1730000000,
		Message:     json.RawMessage(`[{"type":"forward","data":{"id":"fwd-L1"}}]`),
	})
	if err != nil || !ok {
		t.Fatalf("NormalizeGroupMessage failed: ok=%v err=%v", ok, err)
	}

	if len(msg.Blocks) != 1 || msg.Blocks[0].Type != protocol.BlockForwardTree {
		t.Fatalf("expected top-level forward_tree, got %+v", msg.Blocks)
	}
	l1Node := msg.Blocks[0].ForwardNodes[0]
	if l1Node.SenderName != "L1User" || len(l1Node.Blocks) != 2 {
		t.Fatalf("unexpected L1 node: %+v", l1Node)
	}
	l2Tree := l1Node.Blocks[1]
	if l2Tree.Type != protocol.BlockForwardTree || len(l2Tree.ForwardNodes) != 1 {
		t.Fatalf("expected nested L2 forward_tree, got %+v", l2Tree)
	}
	l2Node := l2Tree.ForwardNodes[0]
	if l2Node.SenderName != "L2User" || len(l2Node.Blocks) != 2 {
		t.Fatalf("unexpected L2 node: %+v", l2Node)
	}
	// Depth 2 hits MaxForwardDepth=2, so fwd-L3 must NOT be fetched.
	if l2Node.Blocks[1].Type != protocol.BlockText || !strings.Contains(l2Node.Blocks[1].Text, "最大展开深度 2") {
		t.Fatalf("expected depth-bounded placeholder at depth 2, got %+v", l2Node.Blocks[1])
	}
	if len(fetcher.calls) != 2 || fetcher.calls[0] != "fwd-L1" || fetcher.calls[1] != "fwd-L2" {
		t.Fatalf("unexpected GetForwardMsg calls: %v", fetcher.calls)
	}
}

func TestFFmpegTranscoderGracefulWhenMissingOrStandardAudio(t *testing.T) {
	t.Parallel()

	transcoder := NewFFmpegAudioTranscoder("definitely-nonexistent-ffmpeg-bin-xyz")

	// Standard OGG passes through untouched.
	ogg := []byte("OggS-standard-opus-payload")
	out, outMIME, err := transcoder(t.Context(), ogg, "audio/ogg")
	if err != nil {
		t.Fatalf("standard audio transcode err = %v", err)
	}
	if !bytes.Equal(out, ogg) || outMIME != "audio/ogg" {
		t.Fatalf("standard audio mutated: mime=%q", outMIME)
	}

	// SILK with missing ffmpeg degrades gracefully without failing.
	silk := []byte("#!SILK_V3payload")
	out, outMIME, err = transcoder(t.Context(), silk, "application/octet-stream")
	if err != nil {
		t.Fatalf("missing ffmpeg should degrade gracefully, got err=%v", err)
	}
	if !bytes.Equal(out, silk) || outMIME != "audio/silk" {
		t.Fatalf("unexpected fallback output: mime=%q", outMIME)
	}
}

func TestNormalizerMentionNameResolution(t *testing.T) {
	t.Parallel()

	norm := NewNormalizer(NormalizerOptions{})

	// First message teaches peer nickname for 10001.
	_, ok, err := norm.NormalizeGroupMessage(t.Context(), OneBotEvent{
		PostType:    "message",
		MessageType: "group",
		SelfID:      99999,
		GroupID:     888888,
		UserID:      10001,
		MessageID:   "m1",
		Sender:      OneBotSender{UserID: 10001, Nickname: "小周"},
		Message:     json.RawMessage(`[{"type":"text","data":{"text":"hi"}}]`),
	})
	if err != nil || !ok {
		t.Fatalf("seed nick failed: ok=%v err=%v", ok, err)
	}

	// Second message mentions both bot (no name field from NapCat) and peer 10001.
	msg, ok, err := norm.NormalizeGroupMessage(t.Context(), OneBotEvent{
		PostType:    "message",
		MessageType: "group",
		SelfID:      99999,
		GroupID:     888888,
		UserID:      10002,
		MessageID:   "m2",
		Sender:      OneBotSender{UserID: 10002, Nickname: "阿杰"},
		Message: json.RawMessage(`[
			{"type":"at","data":{"qq":"99999"}},
			{"type":"text","data":{"text":" 找下 "}},
			{"type":"at","data":{"qq":"10001"}}
		]`),
	})
	if err != nil || !ok {
		t.Fatalf("NormalizeGroupMessage failed: ok=%v err=%v", ok, err)
	}
	if !msg.IsAtBot {
		t.Fatal("expected IsAtBot=true")
	}
	if len(msg.Blocks) != 3 {
		t.Fatalf("blocks = %+v, want 3", msg.Blocks)
	}
	if msg.Blocks[0].TargetName != "我" {
		t.Fatalf("self mention TargetName = %q, want 我", msg.Blocks[0].TargetName)
	}
	if msg.Blocks[2].TargetName != "小周" {
		t.Fatalf("peer mention TargetName = %q, want 小周", msg.Blocks[2].TargetName)
	}
}
