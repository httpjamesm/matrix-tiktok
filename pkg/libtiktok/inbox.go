package libtiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

type Conversation struct {
	ID           string   // 0:1:X:Y
	SourceID     uint64   // 5: from get_by_user_init
	Participants []string // user IDs
}

type Message struct {
	ServerID        uint64
	ConvID          string
	ClientMessageID string
	SenderID        string
	Type            string // "text", "image", "video", "sticker"
	Text            string
	MediaURL        string
	MimeType        string
	TimestampMs     int64
	Reactions       []Reaction
}

// Reaction represents a single emoji reaction on a message and the users who sent it.
// The Emoji field holds the raw emoji string (or text alias) after the "e:" prefix is stripped.
// For example, field-1 value "e:❤️" becomes Emoji "❤️", and "e:love" becomes Emoji "love".
type Reaction struct {
	Emoji   string   // emoji character(s) or text name, e.g. "❤️" or "love"
	UserIDs []string // IDs of users who reacted with this emoji
}

const (
	inboxURL     = "/v2/message/get_by_user_init"
	getByConvURL = "/v1/message/get_by_conversation"
	imAID        = "1988"
)

// ---------------------------------------------------------------------------
// Minimal protobuf wire-format encoder
// ---------------------------------------------------------------------------

const (
	wireVarint = 0
	wireBytes  = 2
)

func pbVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func pbTag(fieldNum int, wireType int) []byte {
	return pbVarint(uint64(fieldNum<<3 | wireType))
}

func pbVarintField(fieldNum int, v uint64) []byte {
	var b []byte
	b = append(b, pbTag(fieldNum, wireVarint)...)
	b = append(b, pbVarint(v)...)
	return b
}

func pbBytesField(fieldNum int, v []byte) []byte {
	var b []byte
	b = append(b, pbTag(fieldNum, wireBytes)...)
	b = append(b, pbVarint(uint64(len(v)))...)
	b = append(b, v...)
	return b
}

func pbStringField(fieldNum int, s string) []byte {
	return pbBytesField(fieldNum, []byte(s))
}

func pbEmbedField(fieldNum int, inner []byte) []byte {
	return pbBytesField(fieldNum, inner)
}

// ---------------------------------------------------------------------------
// Minimal protobuf wire-format decoder
// ---------------------------------------------------------------------------

// protoField holds a single decoded protobuf field value.
// Varint fields use u; bytes/embedded fields use b.
type protoField struct {
	isVarint bool
	u        uint64
	b        []byte
}

type protoMsg map[int][]protoField

func pbDecode(data []byte) (protoMsg, error) {
	msg := make(protoMsg)
	for len(data) > 0 {
		tag, n := consumeVarint(data)
		if n <= 0 {
			return nil, fmt.Errorf("malformed tag varint")
		}
		data = data[n:]

		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		switch wireType {
		case wireVarint:
			v, n := consumeVarint(data)
			if n <= 0 {
				return nil, fmt.Errorf("malformed varint for field %d", fieldNum)
			}
			data = data[n:]
			msg[fieldNum] = append(msg[fieldNum], protoField{isVarint: true, u: v})

		case wireBytes:
			length, n := consumeVarint(data)
			if n <= 0 {
				return nil, fmt.Errorf("malformed length for field %d", fieldNum)
			}
			data = data[n:]
			if uint64(len(data)) < length {
				return nil, fmt.Errorf("truncated bytes for field %d", fieldNum)
			}
			b := make([]byte, length)
			copy(b, data[:length])
			data = data[length:]
			msg[fieldNum] = append(msg[fieldNum], protoField{b: b})

		case 1: // 64-bit fixed
			if len(data) < 8 {
				return nil, fmt.Errorf("truncated 64-bit field %d", fieldNum)
			}
			v := uint64(data[0]) | uint64(data[1])<<8 | uint64(data[2])<<16 |
				uint64(data[3])<<24 | uint64(data[4])<<32 | uint64(data[5])<<40 |
				uint64(data[6])<<48 | uint64(data[7])<<56
			data = data[8:]
			msg[fieldNum] = append(msg[fieldNum], protoField{isVarint: true, u: v})

		case 5: // 32-bit fixed
			if len(data) < 4 {
				return nil, fmt.Errorf("truncated 32-bit field %d", fieldNum)
			}
			v := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
			data = data[4:]
			msg[fieldNum] = append(msg[fieldNum], protoField{isVarint: true, u: uint64(v)})

		default:
			// Unknown wire type — we cannot determine the field's length, so
			// we cannot safely advance past it.  Return the fields decoded so
			// far; callers only read low-numbered fields (1, 6, 8, …) which
			// will already be present before any high-numbered exotic field.
			return msg, nil
		}
	}
	return msg, nil
}

func consumeVarint(data []byte) (uint64, int) {
	var v uint64
	for i, b := range data {
		if i >= 10 {
			return 0, -1
		}
		v |= uint64(b&0x7F) << (7 * uint(i))
		if b&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, -1
}

// helpers to pull typed values from a decoded message

func msgGetUint(m protoMsg, field int) uint64 {
	if fs, ok := m[field]; ok && len(fs) > 0 && fs[0].isVarint {
		return fs[0].u
	}
	return 0
}

func msgGetBytes(m protoMsg, field int) []byte {
	if fs, ok := m[field]; ok && len(fs) > 0 && !fs[0].isVarint {
		return fs[0].b
	}
	return nil
}

func msgGetString(m protoMsg, field int) string {
	return string(msgGetBytes(m, field))
}

// msgGetAllBytes returns every bytes-typed occurrence of field (handles repeated fields).
func msgGetAllBytes(m protoMsg, field int) [][]byte {
	fs, ok := m[field]
	if !ok {
		return nil
	}
	out := make([][]byte, 0, len(fs))
	for _, f := range fs {
		if !f.isVarint {
			out = append(out, f.b)
		}
	}
	return out
}

// extractClientMsgID walks the repeated field-9 KV tag pairs in a decoded
// protobuf message and returns the value of the "s:client_message_id" key,
// or "" when not present. Used by both parseMessageEntry (REST) and
// parseWSFrame (WebSocket).
// parseReactions extracts all field-15 reaction entries from a decoded protobuf message.
//
// Wire layout of each field-15 embedded message:
//
//	1: "e:<emoji>"   – reaction key; we strip the "e:" prefix to get the emoji
//	2: {             – user container
//	     1: {        – repeated per-user entry
//	          1: <userID uint64>
//	          4: "<userID string>"   ← preferred; same value as field 1
//	          3: <unix timestamp seconds>
//	          6: <timestamp microseconds>
//	     }
//	   }
func parseReactions(m protoMsg) []Reaction {
	rawEntries := msgGetAllBytes(m, 15)
	if len(rawEntries) == 0 {
		return nil
	}
	out := make([]Reaction, 0, len(rawEntries))
	for _, raw := range rawEntries {
		rm, err := pbDecode(raw)
		if err != nil {
			continue
		}
		key := msgGetString(rm, 1) // e.g. "e:❤️" or "e:love"
		emoji := strings.TrimPrefix(key, "e:")
		if emoji == "" {
			continue
		}

		var userIDs []string
		if usersContainerRaw := msgGetBytes(rm, 2); usersContainerRaw != nil {
			if uc, err := pbDecode(usersContainerRaw); err == nil {
				for _, ue := range msgGetAllBytes(uc, 1) {
					um, err := pbDecode(ue)
					if err != nil {
						continue
					}
					// field 4 carries the user ID as a decimal string; fall back to
					// field 1 (uint64) when field 4 is absent.
					if uid := msgGetString(um, 4); uid != "" {
						userIDs = append(userIDs, uid)
					} else if uid := msgGetUint(um, 1); uid != 0 {
						userIDs = append(userIDs, strconv.FormatUint(uid, 10))
					}
				}
			}
		}

		out = append(out, Reaction{Emoji: emoji, UserIDs: userIDs})
	}
	return deduplicateReactions(out)
}

// deduplicateReactions collapses reaction entries that share an identical set
// of reacting users, keeping the entry whose Emoji contains non-ASCII runes
// (i.e. an actual unicode emoji glyph) over a plain-text alias.
//
// TikTok encodes each reaction twice on the wire – once as the emoji
// character(s) (e.g. "❤️") and once as a text alias (e.g. "love").
// Because both entries carry exactly the same UserIDs, grouping by that
// fingerprint reliably collapses the duplicates without needing an
// alias-to-emoji lookup table.
func deduplicateReactions(in []Reaction) []Reaction {
	if len(in) <= 1 {
		return in
	}

	isUnicode := func(s string) bool {
		for _, r := range s {
			if r > 0x7F {
				return true
			}
		}
		return false
	}
	fingerprint := func(r Reaction) string {
		return strings.Join(r.UserIDs, "\x00")
	}

	type slot struct {
		idx     int
		isEmoji bool
	}
	seen := make(map[string]slot, len(in))
	out := make([]Reaction, 0, len(in))

	for _, r := range in {
		key := fingerprint(r)
		emoji := isUnicode(r.Emoji)
		if s, ok := seen[key]; ok {
			// Replace the stored entry only when the incoming one is a real
			// unicode emoji and the stored one is a plain-text alias.
			if emoji && !s.isEmoji {
				out[s.idx] = r
				seen[key] = slot{idx: s.idx, isEmoji: true}
			}
		} else {
			seen[key] = slot{idx: len(out), isEmoji: emoji}
			out = append(out, r)
		}
	}
	return out
}

func extractClientMsgID(m protoMsg) string {
	for _, tagRaw := range msgGetAllBytes(m, 9) {
		tag, err := pbDecode(tagRaw)
		if err != nil {
			continue
		}
		if msgGetString(tag, 1) == "s:client_message_id" {
			if id := msgGetString(tag, 2); id != "" {
				return id
			}
		}
	}
	return ""
}

// parseMessageContent decodes the JSON content blob (field 8 in both the REST
// get-by-conversation response and the WebSocket push frame) and returns the
// (msgType, text, mediaURL, mimeType) fields for a Message.
//
// Known aweType values:
//
//	0, 700 → "text"  (REST API uses 0; WebSocket push uses 700)
//	800    → "video" (shared TikTok post)
func parseMessageContent(ctx context.Context, c *Client, contentBytes []byte) (msgType, text, mediaURL, mimeType string) {
	if len(contentBytes) == 0 {
		return
	}
	var content map[string]any
	if err := json.Unmarshal(contentBytes, &content); err != nil {
		return
	}
	aweTypeF, _ := content["aweType"].(float64)
	switch int(aweTypeF) {
	case 0, 700:
		msgType = "text"
		text, _ = content["text"].(string)
	case 800:
		msgType = "video"
		if itemID, _ := content["itemId"].(string); itemID != "" {
			if uid, _ := content["uid"].(string); uid != "" {
				if user, err := c.GetUser(ctx, uid); err == nil && user.UniqueID != "" {
					mediaURL = "https://www.tiktok.com/@" + user.UniqueID + "/video/" + itemID
				}
			}
		}
		text, _ = content["content_title"].(string)
	default:
		msgType = fmt.Sprintf("type_%d", int(aweTypeF))
		text, _ = content["text"].(string)
		zerolog.Ctx(ctx).Warn().
			Int("awe_type", int(aweTypeF)).
			RawJSON("content", contentBytes).
			Msg("Received TikTok message with unrecognised aweType — please open an issue")
	}
	return
}

type metaKV struct{ k, v string }

func buildMetadata(deviceID, msToken, verifyFP string) []metaKV {
	pairs := []metaKV{
		{"aid", imAID},
		{"app_name", "tiktok_web"},
		{"channel", "web"},
		{"device_platform", "web_pc"},
		{"device_id", deviceID},
		{"region", "CA"},
		{"priority_region", "CA"},
		{"os", "mac"},
		{"referer", "https://www.tiktok.com/messages?lang=en"},
		{"root_referer", ""},
		{"cookie_enabled", "true"},
		{"screen_width", "1512"},
		{"screen_height", "982"},
		{"browser_language", "en-US"},
		{"browser_platform", "MacIntel"},
		{"browser_name", "Mozilla"},
		{"browser_online", "true"},
		{"app_language", "en"},
		{"webcast_language", "en"},
		{"tz_name", "America/Toronto"},
		{"is_page_visible", "true"},
		{"focus_state", "true"},
		{"is_fullscreen", "false"},
		{"history_len", "2"},
		{"user_is_login", "true"},
		{"data_collection_enabled", "true"},
		{"from_appID", imAID},
		{"locale", "en"},
		{"user_agent", DEFAULT_USER_AGENT},
	}
	if verifyFP != "" {
		pairs = append(pairs, metaKV{"verifyFp", verifyFP})
	}
	if msToken != "" {
		pairs = append(pairs, metaKV{"Web-Sdk-Ms-Token", msToken})
	}
	pairs = append(pairs, metaKV{
		"browser_version",
		"5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	})
	return pairs
}

func buildInboxPayload(deviceID, msToken, verifyFP string) []byte {
	// field 8: {203: {1: 0}}
	inner203 := pbVarintField(1, 0)
	field8Content := pbEmbedField(203, inner203)

	// field 15: repeated embedded metadata key-value pairs
	var metaBytes []byte
	for _, kv := range buildMetadata(deviceID, msToken, verifyFP) {
		var pair []byte
		pair = append(pair, pbStringField(1, kv.k)...)
		pair = append(pair, pbStringField(2, kv.v)...)
		metaBytes = append(metaBytes, pbEmbedField(15, pair)...)
	}

	// top-level envelope (mirrors reversed_user_init_list_out)
	var msg []byte
	msg = append(msg, pbVarintField(1, 203)...)     // message type: user_init_list
	msg = append(msg, pbVarintField(2, 10001)...)   // sub-command
	msg = append(msg, pbStringField(3, "1.6.0")...) // client version
	msg = append(msg, pbEmbedField(4, nil)...)      // empty options message
	msg = append(msg, pbVarintField(5, 3)...)       // platform flag: web_pc
	msg = append(msg, pbVarintField(6, 0)...)
	msg = append(msg, pbStringField(7, "")...) // git hash (not required)
	msg = append(msg, pbEmbedField(8, field8Content)...)
	msg = append(msg, pbStringField(9, deviceID)...)
	msg = append(msg, pbStringField(11, "web")...)
	msg = append(msg, metaBytes...)
	msg = append(msg, pbVarintField(18, 1)...)
	return msg
}

// ---------------------------------------------------------------------------
// Response parser
// ---------------------------------------------------------------------------

// hasRealMessage returns true when the conversation entry contains a genuine
// last message (vs. a placeholder / empty inbox stub).
// Mirrors the PoC's _has_real_message logic.
func hasRealMessage(entry protoMsg) bool {
	raw := msgGetBytes(entry, 8)
	if len(raw) > 0 && !strings.EqualFold(strings.TrimSpace(string(raw)), "placeholder") {
		return true
	}
	if msgGetUint(entry, 5) != 0 { // last message timestamp
		return true
	}
	if msgGetUint(entry, 4) != 0 { // last message type
		return true
	}
	return false
}

func parseConversationEntry(raw []byte) (Conversation, error) {
	entry, err := pbDecode(raw)
	if err != nil {
		return Conversation{}, fmt.Errorf("decode conversation entry: %w", err)
	}

	convID := msgGetString(entry, 1)
	sourceID := msgGetUint(entry, 5)

	// convID format: "0:1:<userA>:<userB>" — take the last two colon-separated segments
	parts := strings.Split(convID, ":")
	if len(parts) < 2 {
		return Conversation{}, fmt.Errorf("unexpected convID format: %q", convID)
	}
	participants := parts[len(parts)-2:]

	return Conversation{
		ID:           convID,
		SourceID:     sourceID,
		Participants: participants,
	}, nil
}

func parseInboxResponse(body []byte) ([]Conversation, error) {
	top, err := pbDecode(body)
	if err != nil {
		return nil, fmt.Errorf("decode top-level response: %w", err)
	}

	// field 6 -> embedded msg -> field 203 -> embedded msg -> field 1 (repeated entries)
	field6Raw := msgGetBytes(top, 6)
	if field6Raw == nil {
		return nil, fmt.Errorf("field 6 missing in response (status=%d)", msgGetUint(top, 1))
	}

	field6, err := pbDecode(field6Raw)
	if err != nil {
		return nil, fmt.Errorf("decode field 6: %w", err)
	}

	field203Raw := msgGetBytes(field6, 203)
	if field203Raw == nil {
		return nil, fmt.Errorf("field 6.203 missing in response")
	}

	field203, err := pbDecode(field203Raw)
	if err != nil {
		return nil, fmt.Errorf("decode field 6.203: %w", err)
	}

	// field 1 may appear multiple times — each occurrence is one conversation entry
	entryRaws := msgGetAllBytes(field203, 1)
	if len(entryRaws) == 0 {
		return nil, nil // empty inbox
	}

	// deduplicate by conv ID (keep first occurrence), drop placeholder entries
	seen := make(map[string]struct{}, len(entryRaws))
	convs := make([]Conversation, 0, len(entryRaws))
	for _, raw := range entryRaws {
		entry, err := pbDecode(raw)
		if err != nil {
			continue
		}
		if !hasRealMessage(entry) {
			continue
		}
		conv, err := parseConversationEntry(raw)
		if err != nil {
			continue
		}
		if _, dup := seen[conv.ID]; dup {
			continue
		}
		seen[conv.ID] = struct{}{}
		convs = append(convs, conv)
	}
	return convs, nil
}

// ---------------------------------------------------------------------------
// GetInbox
// ---------------------------------------------------------------------------

// GetInbox fetches the authenticated user's conversation list from the TikTok
// IM API. It returns one Conversation per thread, with the other participant's
// user ID in Participants.
func (c *Client) GetInbox(ctx context.Context) ([]Conversation, error) {
	// Extract cookie values we need for the request.
	// rIA already has the full cookie header set at construction time.
	cookie := c.rIA.Header.Get("Cookie")
	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, fmt.Errorf("failed to get universal data: %w", err)
	}

	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, fmt.Errorf("failed to get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, fmt.Errorf("failed to access wid: %w", err)
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	payload := buildInboxPayload(deviceID, msToken, verifyFP)

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(inboxURL)
	if err != nil {
		return nil, fmt.Errorf("post inbox: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("inbox API returned %d: %s", resp.StatusCode(), resp.String())
	}

	convs, err := parseInboxResponse(resp.Body())
	if err != nil {
		return nil, fmt.Errorf("parse inbox response: %w", err)
	}
	return convs, nil
}

// ---------------------------------------------------------------------------
// GetMessages
// ---------------------------------------------------------------------------

// buildGetByConversationPayload constructs the type-301 protobuf request body
// for the get_by_conversation endpoint. sourceID is the uint64 from field 5 of
// the conversation entry (Conversation.SourceID); cursorTsUs is the microsecond
// timestamp cursor from field 25 of the last seen message (0 for the first page).
func buildGetByConversationPayload(deviceID, msToken, verifyFP, convID string, sourceID uint64, count int, cursorTsUs uint64) []byte {

	// inner payload: field 8 → 301
	var inner301 []byte
	inner301 = append(inner301, pbStringField(1, convID)...)        // conversation_id
	inner301 = append(inner301, pbVarintField(2, 1)...)             // direction (1 = fetch latest)
	inner301 = append(inner301, pbVarintField(3, sourceID)...)      // sourceID — routes to correct conversation
	inner301 = append(inner301, pbVarintField(4, 1)...)             // flag
	inner301 = append(inner301, pbVarintField(5, cursorTsUs)...)    // cursor timestamp (µs)
	inner301 = append(inner301, pbVarintField(6, uint64(count))...) // message count

	field8Content := pbEmbedField(301, inner301)

	// field 15: repeated embedded metadata key-value pairs (shared with inbox)
	var metaBytes []byte
	for _, kv := range buildMetadata(deviceID, msToken, verifyFP) {
		var pair []byte
		pair = append(pair, pbStringField(1, kv.k)...)
		pair = append(pair, pbStringField(2, kv.v)...)
		metaBytes = append(metaBytes, pbEmbedField(15, pair)...)
	}

	// top-level envelope — same shape as inbox but type 301
	var msg []byte
	msg = append(msg, pbVarintField(1, 301)...)     // message type: get_by_conversation
	msg = append(msg, pbVarintField(2, 1)...)       // identifier
	msg = append(msg, pbStringField(3, "1.6.0")...) // client version
	msg = append(msg, pbEmbedField(4, nil)...)      // empty options message
	msg = append(msg, pbVarintField(5, 3)...)       // platform flag: web_pc
	msg = append(msg, pbVarintField(6, 0)...)
	msg = append(msg, pbStringField(7, "")...) // git hash (not required)
	msg = append(msg, pbEmbedField(8, field8Content)...)
	msg = append(msg, pbStringField(11, "web")...)
	msg = append(msg, metaBytes...)
	msg = append(msg, pbVarintField(18, 1)...)
	return msg
}

// parseMessageEntry decodes a single message entry from the response.
// It returns the Message and the raw field-25 timestamp (µs) used as the
// pagination cursor.
func parseMessageEntry(ctx context.Context, c *Client, raw []byte) (Message, uint64, error) {
	entry, err := pbDecode(raw)
	if err != nil {
		return Message{}, 0, fmt.Errorf("decode message entry: %w", err)
	}

	convID := msgGetString(entry, 1)
	senderID := strconv.FormatUint(msgGetUint(entry, 7), 10)
	tsMicros := msgGetUint(entry, 4)
	cursorTs := msgGetUint(entry, 25) // field 25: pagination timestamp cursor

	serverID := msgGetUint(entry, 3)

	msgID := extractClientMsgID(entry)

	msgType, text, mediaURL, mimeType := parseMessageContent(ctx, c, msgGetBytes(entry, 8))

	return Message{
		ServerID:        serverID,
		ClientMessageID: msgID,
		ConvID:          convID,
		SenderID:        senderID,
		Type:            msgType,
		Text:            text,
		MediaURL:        mediaURL,
		MimeType:        mimeType,
		TimestampMs:     int64(tsMicros) / 1000,
		Reactions:       parseReactions(entry),
	}, cursorTs, nil
}

// parseGetByConversationResponse decodes the protobuf response body.
// Returns the list of messages and the next-page cursor (field-25 timestamp of
// the oldest/last returned message, as a decimal string).
func parseGetByConversationResponse(ctx context.Context, c *Client, body []byte) ([]Message, string, error) {
	top, err := pbDecode(body)
	if err != nil {
		return nil, "", fmt.Errorf("decode top-level response: %w", err)
	}

	// field 6 → embedded msg → field 301 → field 1 (repeated message entries)
	field6Raw := msgGetBytes(top, 6)
	if field6Raw == nil {
		return nil, "", fmt.Errorf("field 6 missing in response (status=%d)", msgGetUint(top, 1))
	}

	field6, err := pbDecode(field6Raw)
	if err != nil {
		return nil, "", fmt.Errorf("decode field 6: %w", err)
	}

	field301Raw := msgGetBytes(field6, 301)
	if field301Raw == nil {
		return nil, "", fmt.Errorf("field 6.301 missing in response")
	}

	field301, err := pbDecode(field301Raw)
	if err != nil {
		return nil, "", fmt.Errorf("decode field 6.301: %w", err)
	}

	entryRaws := msgGetAllBytes(field301, 1)
	if len(entryRaws) == 0 {
		return nil, "", nil
	}

	messages := make([]Message, 0, len(entryRaws))
	var lastCursorTs uint64
	for i, raw := range entryRaws {
		m, cursorTs, err := parseMessageEntry(ctx, c, raw)
		if err != nil {
			// Log instead of silently dropping so parse regressions are visible.
			fmt.Printf("libtiktok: parseMessageEntry entry %d/%d: %v\n", i+1, len(entryRaws), err)
			continue
		}
		messages = append(messages, m)
		if cursorTs != 0 {
			lastCursorTs = cursorTs
		}
	}

	// Reverse so messages are in chronological order (oldest first).
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	nextCursor := ""
	if lastCursorTs != 0 {
		nextCursor = strconv.FormatUint(lastCursorTs, 10)
	}
	return messages, nextCursor, nil
}

// GetMessages fetches up to 20 messages for the given conversation.
// Pass an empty cursor for the first page; subsequent pages use the cursor
// string returned by the previous call (the field-25 µs timestamp of the
// oldest message in the last batch).
// Returns the messages and the next-page cursor (empty string when exhausted).
func (c *Client) GetMessages(ctx context.Context, conv *Conversation, cursor string) ([]Message, string, error) {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get universal data: %w", err)
	}

	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, "", fmt.Errorf("failed to access wid from appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	var cursorTsUs uint64
	if cursor != "" {
		cursorTsUs, _ = strconv.ParseUint(cursor, 10, 64)
	}

	payload := buildGetByConversationPayload(deviceID, msToken, verifyFP, conv.ID, conv.SourceID, 20, cursorTsUs)

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetBody(payload).
		Post(getByConvURL)
	if err != nil {
		return nil, "", fmt.Errorf("post get_by_conversation: %w", err)
	}
	if resp.IsError() {
		return nil, "", fmt.Errorf("get_by_conversation API returned %d: %s", resp.StatusCode(), resp.String())
	}

	messages, nextCursor, err := parseGetByConversationResponse(ctx, c, resp.Body())
	if err != nil {
		return nil, "", fmt.Errorf("parse get_by_conversation response: %w", err)
	}
	return messages, nextCursor, nil
}
