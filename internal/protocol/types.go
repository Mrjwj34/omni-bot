package protocol

import (
	"encoding/json"
	"maps"
	"slices"
)

const (
	MethodChannelRegister = "channel.register"
	MethodChannelInbound  = "channel.inbound"
	MethodChannelOutbound = "channel.outbound"

	MethodUISubscribe   = "ui.subscribe"
	MethodUIUnsubscribe = "ui.unsubscribe"
	MethodUIEvent       = "ui.event"
	MethodChatSend      = "chat.send"
	MethodRunTrigger    = "run.trigger"
)

const (
	EventTraceStep      = "trace.step"
	EventPADUpdate      = "pad.update"
	EventTimelineAppend = "timeline.append"
	EventConfigUpdated  = "config.updated"
	EventChannelStatus  = "channel.status"
)

type Capability string

const (
	CapReply       Capability = "reply"
	CapReact       Capability = "react"
	CapPoke        Capability = "poke"
	CapVoice       Capability = "voice"
	CapSticker     Capability = "sticker"
	CapMultiBubble Capability = "multi_bubble"
)

type CapabilitySet map[Capability]struct{}

func NewCapabilitySet(caps ...Capability) CapabilitySet {
	set := make(CapabilitySet, len(caps))
	for _, c := range caps {
		if c != "" {
			set[c] = struct{}{}
		}
	}
	return set
}

func (s CapabilitySet) Has(c Capability) bool {
	_, ok := s[c]
	return ok
}

func (s CapabilitySet) Slice() []Capability {
	if len(s) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(s))
}

type ChannelRegisterParams struct {
	ChannelID    string       `json:"channel_id"`
	Kind         string       `json:"kind"`
	Capabilities []Capability `json:"capabilities,omitempty"`
}

type ChannelRegisterResult struct {
	ChannelID string `json:"channel_id"`
	Accepted  bool   `json:"accepted"`
}

type ChannelInboundResult struct {
	Accepted bool `json:"accepted"`
}

// MediaRef points to binary media stored via the HTTP Blob API so WS frames stay lightweight.
type MediaRef struct {
	BlobID     string         `json:"blob_id"`
	MIMEType   string         `json:"mime_type"`
	SizeBytes  int64          `json:"size_bytes,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type BlockType string

const (
	BlockText        BlockType = "text"
	BlockLink        BlockType = "link"
	BlockImage       BlockType = "image"
	BlockAudio       BlockType = "audio"
	BlockVideo       BlockType = "video"
	BlockForwardTree BlockType = "forward_tree"
	BlockMention     BlockType = "mention"
	BlockReplyRef    BlockType = "reply_ref"
)

type ForwardNode struct {
	SenderID    string         `json:"sender_id,omitempty"`
	SenderName  string         `json:"sender_name,omitempty"`
	TimestampMs int64          `json:"timestamp_ms,omitempty"`
	Blocks      []ContentBlock `json:"blocks,omitempty"`
}

type ContentBlock struct {
	Type         BlockType     `json:"type"`
	Text         string        `json:"text,omitempty"`
	URL          string        `json:"url,omitempty"`
	Title        string        `json:"title,omitempty"`
	Description  string        `json:"description,omitempty"`
	Media        *MediaRef     `json:"media,omitempty"`
	TargetID     string        `json:"target_id,omitempty"`
	TargetName   string        `json:"target_name,omitempty"`
	ReplyToMsgID string        `json:"reply_to_msg_id,omitempty"`
	ForwardNodes []ForwardNode `json:"forward_nodes,omitempty"`
}

type InboundMessage struct {
	ChannelID    string         `json:"channel_id"`
	RoomID       string         `json:"room_id"`
	MsgID        string         `json:"msg_id"`
	SenderID     string         `json:"sender_id"`
	SenderName   string         `json:"sender_name"`
	IsBot        bool           `json:"is_bot,omitempty"`
	IsAtBot      bool           `json:"is_at_bot,omitempty"`
	TimestampMs  int64          `json:"timestamp_ms"`
	Blocks       []ContentBlock `json:"blocks"`
	ReplyToMsgID string         `json:"reply_to_msg_id,omitempty"`
}

type UISubscribeParams struct {
	Topics []string `json:"topics,omitempty"`
}

type UISubscribeResult struct {
	Topics []string `json:"topics,omitempty"`
}

type UIEventParams struct {
	Topic       string          `json:"topic"`
	RoomID      string          `json:"room_id,omitempty"`
	TimestampMs int64           `json:"timestamp_ms"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

type ChatSendParams struct {
	ChannelID    string     `json:"channel_id"`
	RoomID       string     `json:"room_id"`
	Text         string     `json:"text"`
	ReplyToMsgID string     `json:"reply_to_msg_id,omitempty"`
	Media        []MediaRef `json:"media,omitempty"`
}

type RunTriggerParams struct {
	RoomID     string          `json:"room_id"`
	Source     string          `json:"source,omitempty"`
	BypassGate bool            `json:"bypass_gate,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}
