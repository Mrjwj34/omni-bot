package protocol

import (
	"strings"
)

type ActionType string

const (
	ActionSendText  ActionType = "send_text"
	ActionSendMeme  ActionType = "send_meme"
	ActionSendVoice ActionType = "send_voice"
	ActionReact     ActionType = "react"
	ActionPoke      ActionType = "poke"
)

type OutboundAction struct {
	Type         ActionType `json:"type"`
	Text         string     `json:"text,omitempty"`
	ReplyToMsgID string     `json:"reply_to_msg_id,omitempty"`
	MemeRef      string     `json:"meme_ref,omitempty"`
	Media        *MediaRef  `json:"media,omitempty"`
	MsgID        string     `json:"msg_id,omitempty"`
	Emoji        string     `json:"emoji,omitempty"`
	UserID       string     `json:"user_id,omitempty"`
}

type ChannelOutboundParams struct {
	ChannelID string           `json:"channel_id"`
	RoomID    string           `json:"room_id"`
	Actions   []OutboundAction `json:"actions"`
}

type ChannelOutboundResult struct {
	Delivered int      `json:"delivered"`
	MsgIDs    []string `json:"msg_ids,omitempty"`
}

type tagKind int

const (
	tagNone tagKind = iota
	tagSilent
	tagReply
	tagMeme
	tagVoice
	tagReact
	tagPoke
)

type parsedTag struct {
	kind   tagKind
	arg1   string
	arg2   string
	rawEnd int
}

// ParseOutbound parses model inline output syntax into platform-agnostic OutboundActions.
// If [silent] appears anywhere as a protocol tag, all outbound actions are suppressed and silent is true.
func ParseOutbound(raw string) (actions []OutboundAction, silent bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}

	bubbles, foundSilent := splitBubbles(raw)
	if foundSilent {
		return nil, true
	}

	for _, bubble := range bubbles {
		bubbleActions := parseBubble(bubble)
		actions = append(actions, bubbleActions...)
	}
	if len(actions) == 0 {
		return nil, false
	}
	return actions, false
}

// DegradeActions adapts outbound actions to the capabilities declared by a target adapter.
func DegradeActions(actions []OutboundAction, caps CapabilitySet) []OutboundAction {
	if len(actions) == 0 {
		return nil
	}

	filtered := make([]OutboundAction, 0, len(actions))
	for _, src := range actions {
		act := src
		if src.Media != nil {
			m := *src.Media
			act.Media = &m
		}
		if !caps.Has(CapReply) {
			act.ReplyToMsgID = ""
		}

		switch act.Type {
		case ActionReact:
			if !caps.Has(CapReact) {
				continue
			}
		case ActionPoke:
			if !caps.Has(CapPoke) {
				continue
			}
		case ActionSendMeme:
			if !caps.Has(CapSticker) {
				continue
			}
		case ActionSendVoice:
			if !caps.Has(CapVoice) {
				if strings.TrimSpace(act.Text) == "" {
					continue
				}
				act.Type = ActionSendText
				act.Media = nil
			}
		case ActionSendText:
			if strings.TrimSpace(act.Text) == "" {
				continue
			}
		}
		filtered = append(filtered, act)
	}

	if len(filtered) == 0 {
		return nil
	}
	if caps.Has(CapMultiBubble) {
		return filtered
	}

	merged := make([]OutboundAction, 0, len(filtered))
	for _, act := range filtered {
		if act.Type == ActionSendText && len(merged) > 0 && merged[len(merged)-1].Type == ActionSendText {
			last := &merged[len(merged)-1]
			last.Text += "\n" + act.Text
			if last.ReplyToMsgID == "" {
				last.ReplyToMsgID = act.ReplyToMsgID
			}
			continue
		}
		merged = append(merged, act)
	}
	return merged
}

func splitBubbles(raw string) ([]string, bool) {
	var bubbles []string
	segStart := 0
	for i := 0; i < len(raw); {
		if raw[i] == '[' {
			if tag, ok := matchTag(raw, i); ok {
				if tag.kind == tagSilent {
					return nil, true
				}
				i = tag.rawEnd
				continue
			}
		}
		if strings.HasPrefix(raw[i:], "|||") {
			bubbles = append(bubbles, raw[segStart:i])
			i += 3
			segStart = i
			continue
		}
		i++
	}
	bubbles = append(bubbles, raw[segStart:])
	return bubbles, false
}

func parseBubble(bubble string) []OutboundAction {
	var (
		actions       []OutboundAction
		sideEffects   []OutboundAction
		textBuf       strings.Builder
		pendingReply  string
		replyAttached bool
		justStripped  bool
	)

	attachReply := func(act *OutboundAction) {
		if pendingReply != "" && !replyAttached {
			act.ReplyToMsgID = pendingReply
			replyAttached = true
		}
	}

	flushText := func() {
		trimmed := strings.TrimSpace(textBuf.String())
		textBuf.Reset()
		if trimmed != "" {
			act := OutboundAction{
				Type: ActionSendText,
				Text: trimmed,
			}
			attachReply(&act)
			actions = append(actions, act)
		}
		if len(sideEffects) > 0 {
			actions = append(actions, sideEffects...)
			sideEffects = sideEffects[:0]
		}
		justStripped = false
	}

	appendText := func(chunk string) {
		if chunk == "" {
			return
		}
		if justStripped && strings.HasSuffix(textBuf.String(), " ") && strings.HasPrefix(chunk, " ") {
			chunk = chunk[1:]
		}
		textBuf.WriteString(chunk)
		justStripped = false
	}

	literalStart := 0
	for i := 0; i < len(bubble); {
		if bubble[i] != '[' {
			i++
			continue
		}
		tag, ok := matchTag(bubble, i)
		if !ok {
			i++
			continue
		}

		appendText(bubble[literalStart:i])

		switch tag.kind {
		case tagReply:
			pendingReply = tag.arg1
			justStripped = true
		case tagReact:
			act := OutboundAction{
				Type:  ActionReact,
				MsgID: tag.arg1,
				Emoji: tag.arg2,
			}
			if strings.TrimSpace(textBuf.String()) == "" {
				actions = append(actions, act)
			} else {
				sideEffects = append(sideEffects, act)
			}
			justStripped = true
		case tagPoke:
			act := OutboundAction{
				Type:   ActionPoke,
				UserID: tag.arg1,
			}
			if strings.TrimSpace(textBuf.String()) == "" {
				actions = append(actions, act)
			} else {
				sideEffects = append(sideEffects, act)
			}
			justStripped = true
		case tagMeme:
			flushText()
			act := OutboundAction{
				Type:    ActionSendMeme,
				MemeRef: tag.arg1,
			}
			attachReply(&act)
			actions = append(actions, act)
		case tagVoice:
			flushText()
			if tag.arg1 != "" {
				act := OutboundAction{
					Type: ActionSendVoice,
					Text: tag.arg1,
				}
				attachReply(&act)
				actions = append(actions, act)
			}
		}

		i = tag.rawEnd
		literalStart = i
	}

	appendText(bubble[literalStart:])
	flushText()

	// Support trailing [reply:<id>] placed after a bubble's message body.
	if pendingReply != "" && !replyAttached {
		for idx := range actions {
			if isMessageAction(actions[idx].Type) {
				actions[idx].ReplyToMsgID = pendingReply
				break
			}
		}
	}

	return actions
}

func isMessageAction(t ActionType) bool {
	return t == ActionSendText || t == ActionSendMeme || t == ActionSendVoice
}

func matchTag(s string, start int) (parsedTag, bool) {
	if start >= len(s) || s[start] != '[' {
		return parsedTag{}, false
	}
	rest := s[start+1:]

	if hasFoldPrefix(rest, "silent]") {
		return parsedTag{
			kind:   tagSilent,
			rawEnd: start + 1 + len("silent]"),
		}, true
	}

	if hasFoldPrefix(rest, "voice:") {
		bodyStart := start + 1 + len("voice:")
		end := findClosingBracket(s, bodyStart)
		if end == -1 {
			return parsedTag{}, false
		}
		return parsedTag{
			kind:   tagVoice,
			arg1:   strings.TrimSpace(s[bodyStart:end]),
			rawEnd: end + 1,
		}, true
	}

	closeIdx := strings.IndexByte(rest, ']')
	if closeIdx == -1 {
		return parsedTag{}, false
	}
	inner := rest[:closeIdx]
	rawEnd := start + 1 + closeIdx + 1

	switch {
	case hasFoldPrefix(inner, "reply:"):
		id := strings.TrimSpace(inner[len("reply:"):])
		if id == "" {
			return parsedTag{}, false
		}
		return parsedTag{kind: tagReply, arg1: id, rawEnd: rawEnd}, true

	case hasFoldPrefix(inner, "meme:"):
		ref := strings.TrimSpace(inner[len("meme:"):])
		hash, ok := strings.CutPrefix(ref, "#")
		hash = strings.TrimSpace(hash)
		if !ok || hash == "" {
			return parsedTag{}, false
		}
		return parsedTag{kind: tagMeme, arg1: hash, rawEnd: rawEnd}, true

	case hasFoldPrefix(inner, "react:"):
		payload := inner[len("react:"):]
		msgID, emoji, ok := strings.Cut(payload, ":")
		msgID = strings.TrimSpace(msgID)
		emoji = strings.TrimSpace(emoji)
		if !ok || msgID == "" || emoji == "" {
			return parsedTag{}, false
		}
		return parsedTag{kind: tagReact, arg1: msgID, arg2: emoji, rawEnd: rawEnd}, true

	case hasFoldPrefix(inner, "poke:"):
		uid := strings.TrimSpace(inner[len("poke:"):])
		if uid == "" {
			return parsedTag{}, false
		}
		return parsedTag{kind: tagPoke, arg1: uid, rawEnd: rawEnd}, true
	}

	return parsedTag{}, false
}

// findClosingBracket tracks nested brackets so [voice:哈哈[笑哭]太好笑了] stays intact.
func findClosingBracket(s string, from int) int {
	depth := 1
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
