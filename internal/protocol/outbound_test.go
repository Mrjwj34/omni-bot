package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseOutbound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		want       []OutboundAction
		wantSilent bool
	}{
		{
			name:       "empty string",
			raw:        "   \n\t  ",
			want:       nil,
			wantSilent: false,
		},
		{
			name:       "silent alone",
			raw:        "[silent]",
			want:       nil,
			wantSilent: true,
		},
		{
			name:       "silent anywhere uppercase suppresses all",
			raw:        "hello ||| [SILENT] ||| [poke:10086]",
			want:       nil,
			wantSilent: true,
		},
		{
			name: "plain text trimmed",
			raw:  "  晚上好呀  ",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "晚上好呀"},
			},
		},
		{
			name: "multi bubble with empty bubbles",
			raw:  "||| 第一条 |||   ||| 第二条 |||",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "第一条"},
				{Type: ActionSendText, Text: "第二条"},
			},
		},
		{
			name: "reply attached at start",
			raw:  "[reply:m-101] 确实如此",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "确实如此", ReplyToMsgID: "m-101"},
			},
		},
		{
			name: "reply attached at end",
			raw:  "确实如此 [reply:m-101]",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "确实如此", ReplyToMsgID: "m-101"},
			},
		},
		{
			name: "reply with meme and text",
			raw:  "[reply:m-200] [meme:#a1b2c3] ||| 哈哈哈",
			want: []OutboundAction{
				{Type: ActionSendMeme, MemeRef: "a1b2c3", ReplyToMsgID: "m-200"},
				{Type: ActionSendText, Text: "哈哈哈"},
			},
		},
		{
			name: "mixed text and meme in single bubble",
			raw:  "[reply:m-1] 先看这个 [meme:#deadbeef] 笑死",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "先看这个", ReplyToMsgID: "m-1"},
				{Type: ActionSendMeme, MemeRef: "deadbeef"},
				{Type: ActionSendText, Text: "笑死"},
			},
		},
		{
			name: "voice with nested brackets and inner pipe delimiter",
			raw:  "[reply:m-9] [voice:喂[笑哭] ||| 听得见吗] ||| 收到请回复",
			want: []OutboundAction{
				{Type: ActionSendVoice, Text: "喂[笑哭] ||| 听得见吗", ReplyToMsgID: "m-9"},
				{Type: ActionSendText, Text: "收到请回复"},
			},
		},
		{
			name: "react and poke side effects",
			raw:  "[react:m-42:👍] [poke:u-7] 醒醒啦",
			want: []OutboundAction{
				{Type: ActionReact, MsgID: "m-42", Emoji: "👍"},
				{Type: ActionPoke, UserID: "u-7"},
				{Type: ActionSendText, Text: "醒醒啦"},
			},
		},
		{
			name: "react with colons in emoji shortcode inline inside text",
			raw:  "好的 [react:m-42::thumbsup:] 没问题",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "好的 没问题"},
				{Type: ActionReact, MsgID: "m-42", Emoji: ":thumbsup:"},
			},
		},
		{
			name: "unknown bracketed expressions preserved verbatim",
			raw:  "哈哈 [狗头] arr[0] [1/2] [meme:nohash] [react:broken]",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "哈哈 [狗头] arr[0] [1/2] [meme:nohash] [react:broken]"},
			},
		},
		{
			name: "unclosed voice falls back to literal text",
			raw:  "他说 [voice:未闭合 ||| 下一条",
			want: []OutboundAction{
				{Type: ActionSendText, Text: "他说 [voice:未闭合"},
				{Type: ActionSendText, Text: "下一条"},
			},
		},
		{
			name: "dangling reply and empty voice ignored",
			raw:  "[reply:m-1] ||| [voice:   ]",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, silent := ParseOutbound(tc.raw)
			if silent != tc.wantSilent {
				t.Fatalf("silent = %v, want %v", silent, tc.wantSilent)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseOutbound(%q) =\n%#v\nwant:\n%#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestDegradeActions(t *testing.T) {
	t.Parallel()

	sample := []OutboundAction{
		{Type: ActionSendText, Text: "第一句", ReplyToMsgID: "m-1"},
		{Type: ActionReact, MsgID: "m-1", Emoji: "👍"},
		{Type: ActionPoke, UserID: "u-7"},
		{Type: ActionSendVoice, Text: "语音内容", ReplyToMsgID: "m-2"},
		{Type: ActionSendMeme, MemeRef: "abc123"},
		{Type: ActionSendText, Text: "最后一句"},
	}

	tests := []struct {
		name string
		in   []OutboundAction
		caps CapabilitySet
		want []OutboundAction
	}{
		{
			name: "full capabilities preserves all",
			in:   sample,
			caps: NewCapabilitySet(CapReply, CapReact, CapPoke, CapVoice, CapSticker, CapMultiBubble),
			want: sample,
		},
		{
			name: "zero capabilities degrades voice to text, drops unsupported, merges bubbles",
			in:   sample,
			caps: NewCapabilitySet(),
			want: []OutboundAction{
				{Type: ActionSendText, Text: "第一句\n语音内容\n最后一句"},
			},
		},
		{
			name: "reply only without multi_bubble keeps first reply ref on merged text",
			in: []OutboundAction{
				{Type: ActionSendText, Text: "a", ReplyToMsgID: "m-1"},
				{Type: ActionSendText, Text: "b", ReplyToMsgID: "m-2"},
			},
			caps: NewCapabilitySet(CapReply),
			want: []OutboundAction{
				{Type: ActionSendText, Text: "a\nb", ReplyToMsgID: "m-1"},
			},
		},
		{
			name: "reply inherited from second bubble when first has none",
			in: []OutboundAction{
				{Type: ActionSendText, Text: "a"},
				{Type: ActionSendVoice, Text: "b", ReplyToMsgID: "m-2"},
			},
			caps: NewCapabilitySet(CapReply),
			want: []OutboundAction{
				{Type: ActionSendText, Text: "a\nb", ReplyToMsgID: "m-2"},
			},
		},
		{
			name: "sticker supported prevents merging across meme boundary",
			in: []OutboundAction{
				{Type: ActionSendText, Text: "before"},
				{Type: ActionSendMeme, MemeRef: "f00d"},
				{Type: ActionSendText, Text: "after1"},
				{Type: ActionSendText, Text: "after2"},
			},
			caps: NewCapabilitySet(CapSticker),
			want: []OutboundAction{
				{Type: ActionSendText, Text: "before"},
				{Type: ActionSendMeme, MemeRef: "f00d"},
				{Type: ActionSendText, Text: "after1\nafter2"},
			},
		},
		{
			name: "only unsupported actions degrades to nil",
			in: []OutboundAction{
				{Type: ActionReact, MsgID: "m-1", Emoji: "🔥"},
				{Type: ActionPoke, UserID: "u-9"},
			},
			caps: NewCapabilitySet(CapReply, CapMultiBubble),
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			origCopy := append([]OutboundAction(nil), tc.in...)
			got := DegradeActions(tc.in, tc.caps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DegradeActions() =\n%#v\nwant:\n%#v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.in, origCopy) {
				t.Fatalf("DegradeActions mutated input slice")
			}
		})
	}
}

func TestChannelOutboundParamsRoundTrip(t *testing.T) {
	t.Parallel()

	params := ChannelOutboundParams{
		ChannelID: "qq-main",
		RoomID:    "group-1",
		Actions: []OutboundAction{
			{Type: ActionSendText, Text: "hi", ReplyToMsgID: "m-1"},
			{Type: ActionSendMeme, MemeRef: "abc", Media: &MediaRef{BlobID: "b-9", MIMEType: "image/gif", SizeBytes: 1234}},
		},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ChannelOutboundParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, params) {
		t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", got, params)
	}
}
