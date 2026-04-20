package libtiktok

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/coder/websocket"
	lz4 "github.com/pierrec/lz4/v4"
	"github.com/rs/zerolog"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

// WSEvent is the unit of communication between libtiktok and the connector
// layer. At most one of Message, Reaction, or Deletion is non-nil.
type WSEvent struct {
	Message  *WSMessage
	Reaction *WSReactionEvent
	Deletion *WSMessageDeletion
}

// WSMessage carries a single inbound chat event.
type WSMessage struct {
	Conversation Conversation
	Message      Message
}

// WSReactionEvent carries a reaction property-modify update from the WebSocket.
type WSReactionEvent struct {
	ConversationID  string
	ServerMessageID uint64
	SenderUserID    string // TikTok numeric user ID of the person who reacted
	Modifications   []ReactionModification
}

// ReactionModification is one add/remove entry inside a property_modify JSON.
type ReactionModification struct {
	Op    int    // 0 = add, 1 = remove
	Emoji string // raw emoji character(s), e.g. "❤️"
}

// WSMessageDeletion is a type-500 WebSocket payload describing either:
// - a local hide/delete-for-self command (`content_json.command_type=2`)
// - a global recall/delete-for-everybody event (`s:recall_uid` tag shape)
type WSMessageDeletion struct {
	ConversationID   string
	DeletedMessageID uint64 // TikTok server message id of the removed message
	DeleterUserID    string // numeric TikTok user id from the WS detail, if present
	TimestampMs      int64
	OnlyForMe        bool
}

// ────────────────────────────────────────────────────────────────────────────
// WebSocket URL derivation
// ────────────────────────────────────────────────────────────────────────────

// deriveWSURL assembles the wss://im-ws-sg.tiktok.com/ws/v2 URL that the
// TikTok web client connects to for real-time IM events.
//
// The access_key is derived as:
//
//	MD5("9e1bd35ec9db7b8d846de66ed140b1ad9" + wid + "f8a69f1719916z")
//
// where wid comes from the __UNIVERSAL_DATA_FOR_REHYDRATION__ script tag
// already parsed by getMessagesUniversalData / getAppContext.
func (c *Client) deriveWSURL() (string, error) {
	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return "", fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return "", fmt.Errorf("get app context: %w", err)
	}

	wid, ok := appContext["wid"].(string)
	if !ok || wid == "" {
		return "", fmt.Errorf("wid not found in rehydration app context")
	}

	// access_key = lowercase-hex(MD5("9e1bd35ec9db7b8d846de66ed140b1ad9{wid}f8a69f1719916z"))
	raw := "9e1bd35ec9db7b8d846de66ed140b1ad9" + wid + "f8a69f1719916z"
	sum := md5.Sum([]byte(raw))
	accessKey := hex.EncodeToString(sum[:])

	cookie := c.rIA.Header.Get("Cookie")
	rawTTWid := extractCookie(cookie, "ttwid")
	if rawTTWid == "" {
		return "", fmt.Errorf("ttwid not found in cookie string")
	}
	// The cookie string stores ttwid pre-encoded (e.g. "1%7C...").
	// url.Values.Encode would encode it a second time (%7C → %257C), so we
	// decode it first and let Encode apply a single, correct encoding pass.
	ttwid, err := url.QueryUnescape(rawTTWid)
	if err != nil {
		// Not valid percent-encoding — use the raw value as-is.
		ttwid = rawTTWid
	}

	params := url.Values{
		"device_platform": {"web"},
		"version_code":    {"fws_1.0.0"},
		"access_key":      {accessKey},
		"fpid":            {"9"},
		"aid":             {"1459"},
		"ttwid":           {ttwid},
		"xsack":           {"1"},
		"xaack":           {"1"},
		"xsqos":           {"0"},
	}
	return "wss://im-ws-sg.tiktok.com/ws/v2?" + params.Encode(), nil
}

// ────────────────────────────────────────────────────────────────────────────
// LZ4 outer envelope decompressor
// ────────────────────────────────────────────────────────────────────────────

// lz4CompressionTag is the value of outer-envelope field 6 when the payload is
// LZ4-block-compressed.
var lz4CompressionTag = []byte("__lz4")

// decompressOuterEnvelope parses the WebSocket outer envelope, checks for LZ4
// compression, decompresses if needed, and returns the raw inner protobuf bytes.
func decompressOuterEnvelope(data []byte) ([]byte, error) {
	var outer tiktokpb.WebsocketOuterFrame
	if err := unmarshalProto(data, &outer); err != nil {
		return nil, fmt.Errorf("decode outer envelope: %w", err)
	}

	payload := outer.GetPayload()
	if len(payload) == 0 {
		return nil, nil
	}

	if !bytes.Equal(outer.GetCompression(), lz4CompressionTag) {
		return payload, nil
	}

	return lz4BlockDecompress(payload)
}

// lz4BlockDecompress decompresses an LZ4 raw block. Because the uncompressed
// size is not carried in-band, we try increasingly large buffers until
// decompression succeeds.
func lz4BlockDecompress(src []byte) ([]byte, error) {
	for mult := 4; mult <= 256; mult *= 2 {
		dst := make([]byte, len(src)*mult)
		n, err := lz4.UncompressBlock(src, dst)
		if err != nil {
			continue
		}
		return dst[:n], nil
	}
	return nil, fmt.Errorf("LZ4 block decompress failed: output buffer exhausted")
}

// ────────────────────────────────────────────────────────────────────────────
// Protobuf frame parser
// ────────────────────────────────────────────────────────────────────────────

// parseWSFrame decodes one binary WebSocket frame: first the outer LZ4
// transport envelope, then the inner protobuf. Returns a *WSEvent when the
// frame carries a chat or reaction event, or (nil, nil) for heartbeats.
func (c *Client) parseWSFrame(ctx context.Context, data []byte) (*WSEvent, error) {
	log := zerolog.Ctx(ctx)

	inner, err := decompressOuterEnvelope(data)
	if err != nil {
		return nil, err
	}
	if len(inner) == 0 {
		return nil, nil
	}

	log.Trace().
		Int("outer_bytes", len(data)).
		Int("inner_bytes", len(inner)).
		Msg("WS frame decompressed")

	// The outer envelope's field 8 (payload) contains the WebsocketEnvelope
	// directly — NOT a WebsocketFrame wrapping it at another field 8.
	var env tiktokpb.WebsocketEnvelope
	if err := unmarshalProto(inner, &env); err != nil {
		return nil, fmt.Errorf("decode inner envelope: %w", err)
	}

	innerType := env.GetInnerType()
	if innerType == 0 && env.GetCommands() == nil {
		log.Trace().Msg("WS frame has no inner type or commands (heartbeat/control)")
		return nil, nil
	}

	log.Debug().
		Uint64("inner_type", innerType).
		Int("frame_bytes", len(data)).
		Msg("WS inner frame decoded")

	switch innerType {
	case 500:
		return c.parseChatEvent(ctx, &env)
	case 705:
		return c.parsePropertyUpdateEvent(ctx, &env)
	default:
		if innerType != 0 {
			log.Debug().
				Uint64("inner_type", innerType).
				Int("frame_bytes", len(data)).
				Msg("Unhandled WS inner type — skipping")
		}
		return nil, nil
	}
}

// wsContentCommandJSON is a non-chat command envelope in
// WebsocketMessageDetail.content_json (distinct from aweType message bodies).
type wsContentCommandJSON struct {
	CommandType    int    `json:"command_type"`
	ConversationID string `json:"conversation_id"`
	MessageID      uint64 `json:"message_id"`
}

// TikTok uses command_type=2 in WS content_json for delete-for-self.
const wsContentCommandDelete = 2

// tryParseWSDeleteForSelf returns handled=true when content_json is a local
// delete/hide command. If handled and the returned *WSEvent is nil, the payload
// was malformed and the frame must not be interpreted as a normal chat message.
func tryParseWSDeleteForSelf(chat *tiktokpb.WebsocketChat, detail *tiktokpb.WebsocketMessageDetail) (handled bool, evt *WSEvent) {
	if detail == nil {
		return false, nil
	}
	content := detail.GetContentJson()
	if len(content) == 0 {
		return false, nil
	}
	var cmd wsContentCommandJSON
	if err := json.Unmarshal(content, &cmd); err != nil {
		return false, nil
	}
	if cmd.CommandType != wsContentCommandDelete {
		return false, nil
	}
	if cmd.MessageID == 0 {
		return true, nil
	}
	convID := cmd.ConversationID
	if convID == "" && chat != nil {
		convID = chat.GetConversationId()
	}
	if convID == "" {
		return true, nil
	}
	deleter := ""
	if uid := detail.GetSenderUserId(); uid != 0 {
		deleter = strconv.FormatUint(uid, 10)
	}
	tsMs := int64(detail.GetTimestampUs() / 1000)
	return true, &WSEvent{
		Deletion: &WSMessageDeletion{
			ConversationID:   convID,
			DeletedMessageID: cmd.MessageID,
			DeleterUserID:    deleter,
			TimestampMs:      tsMs,
			OnlyForMe:        true,
		},
	}
}

// tryParseWSDeleteForEveryone returns handled=true when the tag list matches
// the recall/delete-for-everybody event shape. If handled and the returned
// *WSEvent is nil, the frame is malformed and must not be interpreted as a
// normal chat message.
func tryParseWSDeleteForEveryone(chat *tiktokpb.WebsocketChat, detail *tiktokpb.WebsocketMessageDetail) (handled bool, evt *WSEvent) {
	if chat == nil || detail == nil {
		return false, nil
	}

	convID := chat.GetConversationId()
	if convID == "" {
		return false, nil
	}

	var deletedMsgID uint64
	var deleter string
	hasRecallUID := false
	for _, tag := range detail.GetTags() {
		switch tag.GetKey() {
		case "s:recall_uid":
			hasRecallUID = true
			deleter = string(tag.GetValue())
		case "s:server_message_id":
			deletedMsgID, _ = strconv.ParseUint(string(tag.GetValue()), 10, 64)
		}
	}
	if !hasRecallUID {
		return false, nil
	}
	if deletedMsgID == 0 {
		return true, nil
	}
	if deleter == "" && detail.GetSenderUserId() != 0 {
		deleter = strconv.FormatUint(detail.GetSenderUserId(), 10)
	}

	return true, &WSEvent{
		Deletion: &WSMessageDeletion{
			ConversationID:   convID,
			DeletedMessageID: deletedMsgID,
			DeleterUserID:    deleter,
			TimestampMs:      int64(detail.GetTimestampUs() / 1000),
			OnlyForMe:        false,
		},
	}
}

// parseChatEvent handles inner_type 500, which carries both regular chat
// messages and property updates (reactions). If the detail tags contain
// s:property_modify, the frame is a reaction event, not a chat message.
func (c *Client) parseChatEvent(ctx context.Context, env *tiktokpb.WebsocketEnvelope) (*WSEvent, error) {
	log := zerolog.Ctx(ctx)

	chat := env.GetCommands().GetChat()
	if chat == nil {
		return nil, nil
	}

	convID := chat.GetConversationId()
	if convID == "" {
		return nil, fmt.Errorf("conversation ID missing from chat container")
	}

	detail := chat.GetDetail()
	if detail == nil {
		return nil, fmt.Errorf("message detail (chat.5) missing")
	}

	// Check tags for s:property_modify — if present this is a reaction event
	// disguised as a type-500 frame, not a real chat message.
	if evt := c.tryParseReactionFromTags(ctx, convID, detail.GetTags()); evt != nil {
		return evt, nil
	}

	if handled, delEvt := tryParseWSDeleteForEveryone(chat, detail); handled {
		if delEvt != nil {
			log.Debug().
				Str("conv_id", delEvt.Deletion.ConversationID).
				Uint64("deleted_message_id", delEvt.Deletion.DeletedMessageID).
				Bool("only_for_me", delEvt.Deletion.OnlyForMe).
				Msg("WS 500: message recall/delete-for-everyone event")
			return delEvt, nil
		}
		log.Warn().
			Str("conv_id", convID).
			Msg("WS 500: message recall event missing target message ID — dropping frame")
		return nil, nil
	}

	if handled, delEvt := tryParseWSDeleteForSelf(chat, detail); handled {
		if delEvt != nil {
			log.Debug().
				Str("conv_id", delEvt.Deletion.ConversationID).
				Uint64("deleted_message_id", delEvt.Deletion.DeletedMessageID).
				Bool("only_for_me", delEvt.Deletion.OnlyForMe).
				Msg("WS 500: message delete-for-self command")
			return delEvt, nil
		}
		log.Warn().
			Str("conv_id", convID).
			Msg("WS 500: message delete-for-self command (command_type=2) missing conversation_id or message_id — dropping frame")
		return nil, nil
	}

	if shouldSkipSyncedMessage(detail.GetTags()) {
		log.Debug().
			Str("conv_id", convID).
			Msg("WS 500: skipping recalled/invisible message row")
		return nil, nil
	}

	numericMsgID := detail.GetServerMessageId()
	tsUs := int64(detail.GetTimestampUs())
	senderID := strconv.FormatUint(detail.GetSenderUserId(), 10)
	msgID := extractClientMsgIDFromTags(detail.GetTags())
	wsContent := detail.GetContentJson()
	msgType, text, mediaURL, mimeType := parseMessageContent(ctx, c, wsContent)
	messageSubtype := detail.GetMessageSubtype()
	thumbURL := ""
	decryptKey := ""
	mediaWidth := 0
	mediaHeight := 0
	mediaDurationMs := 0
	if privateType, assetURL, assetThumbURL, assetDecryptKey, width, height, durationMs, ok := parsePrivateMediaFromWebsocketDetailProto(detail); ok {
		msgType = privateType
		mediaURL = assetURL
		thumbURL = assetThumbURL
		decryptKey = assetDecryptKey
		mimeType = ""
		mediaWidth = width
		mediaHeight = height
		mediaDurationMs = durationMs
		if text == "" {
			if privateType == "video" {
				text = "[video]"
			} else {
				text = "[photo]"
			}
		}
	}
	if stickerURL, stickerText, stickerMIME, ok := parseStickerFromWebsocketDetailProto(detail); ok {
		msgType = "sticker"
		text = stickerText
		mediaURL = stickerURL
		thumbURL = ""
		decryptKey = ""
		mimeType = stickerMIME
		mediaWidth = 0
		mediaHeight = 0
	}
	replyTo := uint64(0)
	replyQuoted := ""
	if ref := detail.GetMessageReply(); ref != nil {
		replyTo = ref.GetReferencedServerMessageId()
		replyQuoted = parseReplyQuotedTextFromWire(ref.GetQuotedContextJson())
	}
	rawJSON := append([]byte(nil), wsContent...)

	dbg := log.Debug().
		Str("conv_id", convID).
		Str("sender_id", senderID).
		Str("msg_type", msgType).
		Uint64("server_msg_id", numericMsgID)
	if replyTo != 0 {
		dbg = dbg.Uint64("reply_to_server_msg_id", replyTo)
	}
	dbg.Msg("WS 500: chat message")

	msg := Message{
		ServerID:        numericMsgID,
		ClientMessageID: msgID,
		ConvID:          convID,
		SenderID:        senderID,
		Type:            msgType,
		MessageSubtype:  messageSubtype,
		Text:            text,
		MediaURL:        mediaURL,
		ThumbnailURL:    thumbURL,
		MediaDecryptKey: decryptKey,
		MimeType:        mimeType,
		MediaWidth:      mediaWidth,
		MediaHeight:     mediaHeight,
		MediaDurationMs: mediaDurationMs,
		TimestampMs:     tsUs / 1000,
		ReplyToServerID: replyTo,
		ReplyQuotedText: replyQuoted,
		SendChainID:     detail.GetSendChainId(),
		SenderSecUID:    detail.GetSenderSecUid(),
		CursorTsUs:      detail.GetCursorTsUs(),
		RawContentJSON:  rawJSON,
	}
	conv := Conversation{
		ID:           convID,
		Participants: []string{senderID},
	}
	return &WSEvent{Message: &WSMessage{Conversation: conv, Message: msg}}, nil
}

// tryParseReactionFromTags checks whether the tag list contains
// s:property_modify. If it does, the frame is a reaction event and we return a
// WSEvent with a Reaction payload. Returns nil when the tags carry no reaction.
func (c *Client) tryParseReactionFromTags(ctx context.Context, convID string, tags []*tiktokpb.MetadataTag) *WSEvent {
	log := zerolog.Ctx(ctx)

	var propertyJSON []byte
	var serverMsgIDStr string
	for _, tag := range tags {
		switch tag.GetKey() {
		case "s:property_modify":
			propertyJSON = tag.GetValue()
		case "s:server_message_id":
			serverMsgIDStr = string(tag.GetValue())
		}
	}
	if len(propertyJSON) == 0 {
		return nil
	}

	log.Debug().
		Str("conv_id", convID).
		Str("property_json", string(propertyJSON)).
		Msg("WS 500: frame contains s:property_modify — treating as reaction")

	var prop propertyModify
	if err := json.Unmarshal(propertyJSON, &prop); err != nil {
		log.Warn().Err(err).
			Str("conv_id", convID).
			Msg("Failed to parse s:property_modify JSON from type-500 frame")
		return nil
	}

	log.Debug().
		Str("conv_id", convID).
		Uint64("user_id", prop.UserId).
		Uint64("server_message_id", prop.ServerMessageId).
		Int("modify_count", len(prop.Modifys)).
		Msg("WS 500: parsed property_modify")

	if len(prop.Modifys) == 0 {
		return nil
	}

	serverMsgID := prop.ServerMessageId
	if serverMsgID == 0 && serverMsgIDStr != "" {
		serverMsgID, _ = strconv.ParseUint(serverMsgIDStr, 10, 64)
	}

	mods := deduplicateModifications(prop.Modifys)
	if len(mods) == 0 {
		return nil
	}

	senderUID := ""
	if prop.UserId != 0 {
		senderUID = strconv.FormatUint(prop.UserId, 10)
	}

	log.Info().
		Str("conv_id", convID).
		Uint64("server_message_id", serverMsgID).
		Str("sender_user_id", senderUID).
		Int("reaction_count", len(mods)).
		Msg("WS 500: reaction event extracted from property_modify")

	return &WSEvent{
		Reaction: &WSReactionEvent{
			ConversationID:  convID,
			ServerMessageID: serverMsgID,
			SenderUserID:    senderUID,
			Modifications:   mods,
		},
	}
}

// parsePropertyUpdateEvent handles inner_type 705 (property mutation), which
// includes reaction adds/removes.
func (c *Client) parsePropertyUpdateEvent(ctx context.Context, env *tiktokpb.WebsocketEnvelope) (*WSEvent, error) {
	log := zerolog.Ctx(ctx)

	pu := env.GetCommands().GetPropertyUpdate()
	if pu == nil {
		log.Debug().Msg("WS 705: property_update field is nil — commands may use a different field number")
		return nil, nil
	}

	convID := pu.GetConversationId()
	if convID == "" {
		log.Debug().Msg("WS 705: property_update has no conversation_id")
		return nil, nil
	}

	var tagKeys []string
	var propertyJSON []byte
	var serverMsgIDStr string
	for _, tag := range pu.GetTags() {
		tagKeys = append(tagKeys, tag.GetKey())
		switch tag.GetKey() {
		case "s:property_modify":
			propertyJSON = tag.GetValue()
		case "s:server_message_id":
			serverMsgIDStr = string(tag.GetValue())
		}
	}

	log.Debug().
		Str("conv_id", convID).
		Strs("tag_keys", tagKeys).
		Int("property_json_len", len(propertyJSON)).
		Str("server_msg_id_tag", serverMsgIDStr).
		Msg("WS 705: property update received")

	if len(propertyJSON) == 0 {
		log.Debug().
			Str("conv_id", convID).
			Msg("WS 705: no s:property_modify tag found")
		return nil, nil
	}

	log.Debug().
		Str("conv_id", convID).
		Str("property_json", string(propertyJSON)).
		Msg("WS 705: raw property_modify JSON")

	var prop propertyModify
	if err := json.Unmarshal(propertyJSON, &prop); err != nil {
		log.Warn().Err(err).
			Str("conv_id", convID).
			Msg("Failed to parse s:property_modify JSON")
		return nil, nil
	}

	log.Debug().
		Str("conv_id", convID).
		Uint64("user_id", prop.UserId).
		Uint64("server_message_id", prop.ServerMessageId).
		Int("modify_count", len(prop.Modifys)).
		Msg("WS 705: parsed property_modify")

	if len(prop.Modifys) == 0 {
		log.Debug().
			Str("conv_id", convID).
			Msg("WS 705: Modifys array is empty")
		return nil, nil
	}

	serverMsgID := prop.ServerMessageId
	if serverMsgID == 0 && serverMsgIDStr != "" {
		serverMsgID, _ = strconv.ParseUint(serverMsgIDStr, 10, 64)
	}

	mods := deduplicateModifications(prop.Modifys)
	if len(mods) == 0 {
		log.Debug().
			Str("conv_id", convID).
			Msg("WS 705: no valid reaction modifications after filtering")
		return nil, nil
	}

	senderUID := ""
	if prop.UserId != 0 {
		senderUID = strconv.FormatUint(prop.UserId, 10)
	}

	log.Info().
		Str("conv_id", convID).
		Uint64("server_message_id", serverMsgID).
		Str("sender_user_id", senderUID).
		Int("reaction_count", len(mods)).
		Msg("WS 705: reaction event parsed successfully")

	return &WSEvent{
		Reaction: &WSReactionEvent{
			ConversationID:  convID,
			ServerMessageID: serverMsgID,
			SenderUserID:    senderUID,
			Modifications:   mods,
		},
	}, nil
}

// propertyModify is the JSON structure carried by s:property_modify in a 705
// WebSocket event. Only the fields needed for reaction processing are decoded.
type propertyModify struct {
	UserId          uint64             `json:"UserId"`
	ServerMessageId uint64             `json:"ServerMessageId"`
	Modifys         []propertyModifyOp `json:"Modifys"`
}

type propertyModifyOp struct {
	Op  int    `json:"Op"`
	Key string `json:"Key"`
}

// deduplicateModifications collapses the duplicate emoji/alias entries that
// TikTok sends for every reaction (e.g. "e:❤️" + "e:love") into a single
// ReactionModification, preferring the Unicode emoji over the text alias.
func deduplicateModifications(ops []propertyModifyOp) []ReactionModification {
	type slot struct {
		idx     int
		isEmoji bool
	}
	seen := make(map[int]slot, len(ops))
	out := make([]ReactionModification, 0, len(ops))

	for _, m := range ops {
		emoji := strings.TrimPrefix(m.Key, "e:")
		if emoji == "" {
			continue
		}
		uni := isUnicodeEmoji(emoji)
		if s, ok := seen[m.Op]; ok {
			if uni && !s.isEmoji {
				out[s.idx] = ReactionModification{Op: m.Op, Emoji: emoji}
				seen[m.Op] = slot{idx: s.idx, isEmoji: true}
			}
		} else {
			seen[m.Op] = slot{idx: len(out), isEmoji: uni}
			out = append(out, ReactionModification{Op: m.Op, Emoji: emoji})
		}
	}
	return out
}

// ────────────────────────────────────────────────────────────────────────────
// Public API
// ────────────────────────────────────────────────────────────────────────────

// ConnectWebSocket dials the TikTok IM WebSocket, starts a background read
// pump goroutine, and returns a channel that emits one WSEvent per inbound
// event (chat message or reaction).
//
// Each binary frame is first decoded through the LZ4 outer envelope (field 6
// = "__lz4" triggers block decompression of field 8), then the inner protobuf
// is parsed and dispatched by inner_type (500 = chat / delete command, 705 =
// property update).
//
// The caller owns the channel lifetime: it is closed when ctx is cancelled or
// the server closes the connection, which is the signal the connector uses to
// trigger a reconnect.  Non-chat frames (heartbeats, ACKs, etc.) are silently
// discarded; parse errors are logged but do not terminate the pump.
func (c *Client) ConnectWebSocket(ctx context.Context) (<-chan WSEvent, error) {
	log := zerolog.Ctx(ctx)

	wsURL, err := c.deriveWSURL()
	if err != nil {
		return nil, fmt.Errorf("derive WS URL: %w", err)
	}
	log.Debug().Str("url", wsURL).Msg("Dialling TikTok IM WebSocket")

	cookie := c.rIA.Header.Get("Cookie")
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Cookie":     {cookie},
			"User-Agent": {DEFAULT_USER_AGENT},
			"Origin":     {"https://www.tiktok.com"},
			"Referer":    {"https://www.tiktok.com/"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("WS dial: %w", err)
	}

	ch := make(chan WSEvent, 32)
	go func() {
		defer close(ch)
		defer conn.CloseNow()

		log.Info().Msg("TikTok IM WebSocket connected")

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Err(err).Msg("TikTok IM WebSocket read error")
				}
				return
			}

			evt, err := c.parseWSFrame(ctx, data)
			if err != nil {
				log.Warn().Err(err).Int("frame_bytes", len(data)).Msg("Failed to parse WS frame")
				continue
			}
			if evt == nil {
				continue
			}

			select {
			case ch <- *evt:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}
