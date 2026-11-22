package protocol

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	req, err := NewRequest("req-1", MethodChannelRegister, ChannelRegisterParams{
		ChannelID:    "qq-main",
		Kind:         "napcat",
		Capabilities: []Capability{CapReply, CapReact, CapPoke, CapVoice, CapSticker, CapMultiBubble},
	})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	intReq, err := NewRequest(42, MethodUISubscribe, UISubscribeParams{
		Topics: []string{EventTraceStep, EventPADUpdate},
	})
	if err != nil {
		t.Fatalf("NewRequest(int): %v", err)
	}

	notif, err := NewNotification(MethodUIEvent, UIEventParams{
		Topic:       EventTimelineAppend,
		RoomID:      "room-1",
		TimestampMs: 1730000000123,
		Payload:     json.RawMessage(`{"seq_id":99}`),
	})
	if err != nil {
		t.Fatalf("NewNotification: %v", err)
	}

	resp, err := NewResponse(req.ID, ChannelRegisterResult{
		ChannelID: "qq-main",
		Accepted:  true,
	})
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}

	errResp, err := NewErrorResponse(req.ID, ErrCodeInvalidParams, "missing channel_id", map[string]string{"field": "channel_id"})
	if err != nil {
		t.Fatalf("NewErrorResponse: %v", err)
	}

	tests := []struct {
		name          string
		env           Envelope
		wantRequest   bool
		wantNotif     bool
		wantResponse  bool
		wantIDKey     string
		wantRPCError  bool
		wantErrCode   int
		verifyDecoded func(t *testing.T, got Envelope)
	}{
		{
			name:        "string id request",
			env:         req,
			wantRequest: true,
			wantIDKey:   `"req-1"`,
			verifyDecoded: func(t *testing.T, got Envelope) {
				var p ChannelRegisterParams
				if err := got.DecodeParams(&p); err != nil {
					t.Fatalf("DecodeParams: %v", err)
				}
				if p.ChannelID != "qq-main" || p.Kind != "napcat" || len(p.Capabilities) != 6 {
					t.Fatalf("unexpected params: %+v", p)
				}
			},
		},
		{
			name:        "numeric id request",
			env:         intReq,
			wantRequest: true,
			wantIDKey:   "42",
			verifyDecoded: func(t *testing.T, got Envelope) {
				var p UISubscribeParams
				if err := got.DecodeParams(&p); err != nil {
					t.Fatalf("DecodeParams: %v", err)
				}
				want := []string{EventTraceStep, EventPADUpdate}
				if !reflect.DeepEqual(p.Topics, want) {
					t.Fatalf("topics = %v, want %v", p.Topics, want)
				}
			},
		},
		{
			name:      "notification without id",
			env:       notif,
			wantNotif: true,
			wantIDKey: "",
			verifyDecoded: func(t *testing.T, got Envelope) {
				var p UIEventParams
				if err := got.DecodeParams(&p); err != nil {
					t.Fatalf("DecodeParams: %v", err)
				}
				if p.Topic != EventTimelineAppend || p.RoomID != "room-1" || p.TimestampMs != 1730000000123 {
					t.Fatalf("unexpected event params: %+v", p)
				}
			},
		},
		{
			name:         "success response",
			env:          resp,
			wantResponse: true,
			wantIDKey:    `"req-1"`,
			verifyDecoded: func(t *testing.T, got Envelope) {
				var r ChannelRegisterResult
				if err := got.DecodeResult(&r); err != nil {
					t.Fatalf("DecodeResult: %v", err)
				}
				if !r.Accepted || r.ChannelID != "qq-main" {
					t.Fatalf("unexpected result: %+v", r)
				}
			},
		},
		{
			name:         "error response",
			env:          errResp,
			wantResponse: true,
			wantIDKey:    `"req-1"`,
			wantRPCError: true,
			wantErrCode:  ErrCodeInvalidParams,
			verifyDecoded: func(t *testing.T, got Envelope) {
				var unused map[string]any
				err := got.DecodeResult(&unused)
				var rpcErr *RPCError
				if !errors.As(err, &rpcErr) {
					t.Fatalf("expected *RPCError, got %v", err)
				}
				if rpcErr.Code != ErrCodeInvalidParams || rpcErr.Message != "missing channel_id" {
					t.Fatalf("unexpected rpc error: %+v", rpcErr)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wire, err := EncodeEnvelope(tc.env)
			if err != nil {
				t.Fatalf("EncodeEnvelope: %v", err)
			}
			got, err := DecodeEnvelope(wire)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if got.JSONRPC != JSONRPCVersion {
				t.Fatalf("JSONRPC = %q, want %q", got.JSONRPC, JSONRPCVersion)
			}
			if got.IsRequest() != tc.wantRequest {
				t.Fatalf("IsRequest() = %v, want %v", got.IsRequest(), tc.wantRequest)
			}
			if got.IsNotification() != tc.wantNotif {
				t.Fatalf("IsNotification() = %v, want %v", got.IsNotification(), tc.wantNotif)
			}
			if got.IsResponse() != tc.wantResponse {
				t.Fatalf("IsResponse() = %v, want %v", got.IsResponse(), tc.wantResponse)
			}
			if got.IDKey() != tc.wantIDKey {
				t.Fatalf("IDKey() = %q, want %q", got.IDKey(), tc.wantIDKey)
			}
			if (got.Error != nil) != tc.wantRPCError {
				t.Fatalf("Error present = %v, want %v", got.Error != nil, tc.wantRPCError)
			}
			if tc.wantRPCError && got.Error.Code != tc.wantErrCode {
				t.Fatalf("Error.Code = %d, want %d", got.Error.Code, tc.wantErrCode)
			}
			tc.verifyDecoded(t, got)
		})
	}
}

func TestDecodeEnvelopeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		wantErr error
	}{
		{
			name:    "malformed json",
			payload: `{"jsonrpc":"2.0"`,
			wantErr: ErrInvalidJSON,
		},
		{
			name:    "wrong version",
			payload: `{"jsonrpc":"1.0","method":"channel.register"}`,
			wantErr: ErrInvalidVersion,
		},
		{
			name:    "missing version",
			payload: `{"method":"channel.register"}`,
			wantErr: ErrInvalidVersion,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeEnvelope([]byte(tc.payload))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("DecodeEnvelope error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestInboundMessageAndBlocksRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  InboundMessage
	}{
		{
			name: "plain text with mention and reply ref",
			msg: InboundMessage{
				ChannelID:    "qq-1",
				RoomID:       "group-1001",
				MsgID:        "m-101",
				SenderID:     "u-42",
				SenderName:   "Alice",
				IsAtBot:      true,
				TimestampMs:  1730000010000,
				ReplyToMsgID: "m-100",
				Blocks: []ContentBlock{
					{Type: BlockReplyRef, ReplyToMsgID: "m-100", TargetID: "u-bot", TargetName: "Bot", Text: "prev"},
					{Type: BlockMention, TargetID: "u-bot", TargetName: "Bot"},
					{Type: BlockText, Text: "帮我看看这个链接"},
					{
						Type:        BlockLink,
						URL:         "https://example.com/post",
						Title:       "Example Post",
						Description: "Summary",
						Media:       &MediaRef{BlobID: "blob-thumb", MIMEType: "image/jpeg", SizeBytes: 4096},
					},
				},
			},
		},
		{
			name: "media blocks without base64",
			msg: InboundMessage{
				ChannelID:   "qq-1",
				RoomID:      "group-1001",
				MsgID:       "m-102",
				SenderID:    "u-43",
				SenderName:  "Bob",
				TimestampMs: 1730000020000,
				Blocks: []ContentBlock{
					{
						Type: BlockImage,
						Media: &MediaRef{
							BlobID:    "sha256-img1",
							MIMEType:  "image/png",
							SizeBytes: 120000,
							Meta:      map[string]any{"width": float64(800), "height": float64(600)},
						},
					},
					{
						Type: BlockAudio,
						Media: &MediaRef{
							BlobID:     "sha256-aud1",
							MIMEType:   "audio/ogg",
							SizeBytes:  34000,
							DurationMs: 5200,
						},
					},
					{
						Type: BlockVideo,
						Media: &MediaRef{
							BlobID:     "sha256-vid1",
							MIMEType:   "video/mp4",
							SizeBytes:  2400000,
							DurationMs: 18000,
							Meta:       map[string]any{"source": "bilibili"},
						},
					},
				},
			},
		},
		{
			name: "nested forward tree",
			msg: InboundMessage{
				ChannelID:   "qq-1",
				RoomID:      "group-1001",
				MsgID:       "m-103",
				SenderID:    "u-44",
				SenderName:  "Carol",
				IsBot:       false,
				TimestampMs: 1730000030000,
				Blocks: []ContentBlock{
					{
						Type:  BlockForwardTree,
						Title: "聊天记录",
						ForwardNodes: []ForwardNode{
							{
								SenderID:    "u-1",
								SenderName:  "Dave",
								TimestampMs: 1730000001000,
								Blocks: []ContentBlock{
									{Type: BlockText, Text: "第一条"},
									{
										Type: BlockForwardTree,
										ForwardNodes: []ForwardNode{
											{
												SenderID:   "u-2",
												SenderName: "Eve",
												Blocks: []ContentBlock{
													{Type: BlockImage, Media: &MediaRef{BlobID: "blob-inner", MIMEType: "image/webp", SizeBytes: 8192}},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			var got InboundMessage
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.msg) {
				t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", got, tc.msg)
			}
		})
	}
}

func TestCapabilitySet(t *testing.T) {
	t.Parallel()

	set := NewCapabilitySet(CapMultiBubble, CapReply, CapVoice, CapReply, "")
	if !set.Has(CapReply) || !set.Has(CapVoice) || !set.Has(CapMultiBubble) {
		t.Fatalf("missing expected capabilities in %v", set)
	}
	if set.Has(CapPoke) || set.Has("") {
		t.Fatalf("unexpected capabilities present in %v", set)
	}
	want := []Capability{CapMultiBubble, CapReply, CapVoice}
	if got := set.Slice(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Slice() = %v, want %v", got, want)
	}
}

func TestWebUIPayloadsRoundTrip(t *testing.T) {
	t.Parallel()

	chatSend := ChatSendParams{
		ChannelID:    "webui",
		RoomID:       "room-1",
		Text:         "hello",
		ReplyToMsgID: "m-1",
		Media:        []MediaRef{{BlobID: "b-1", MIMEType: "image/png", SizeBytes: 512}},
	}
	b, err := json.Marshal(chatSend)
	if err != nil {
		t.Fatalf("marshal ChatSendParams: %v", err)
	}
	var gotChat ChatSendParams
	if err := json.Unmarshal(b, &gotChat); err != nil {
		t.Fatalf("unmarshal ChatSendParams: %v", err)
	}
	if !reflect.DeepEqual(gotChat, chatSend) {
		t.Fatalf("ChatSendParams mismatch: got %+v, want %+v", gotChat, chatSend)
	}

	trigger := RunTriggerParams{
		RoomID:     "room-1",
		Source:     "webui",
		BypassGate: true,
		Payload:    json.RawMessage(`{"note":"manual"}`),
	}
	b, err = json.Marshal(trigger)
	if err != nil {
		t.Fatalf("marshal RunTriggerParams: %v", err)
	}
	var gotTrigger RunTriggerParams
	if err := json.Unmarshal(b, &gotTrigger); err != nil {
		t.Fatalf("unmarshal RunTriggerParams: %v", err)
	}
	if !reflect.DeepEqual(gotTrigger, trigger) {
		t.Fatalf("RunTriggerParams mismatch: got %+v, want %+v", gotTrigger, trigger)
	}
}
