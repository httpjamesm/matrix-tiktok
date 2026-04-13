package libtiktok

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type Conversation struct {
	ID           string
	Participants []string // user IDs
}

type Message struct {
	ID          string
	ConvID      string
	SenderID    string
	Type        string // "text", "image", "video", "sticker"
	Text        string
	MediaURL    string
	MimeType    string
	TimestampMs int64
}

const (
	inboxURL = "/v2/message/get_by_user_init"
	imAID    = "1988"
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
			return nil, fmt.Errorf("unknown wire type %d for field %d", wireType, fieldNum)
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
	userID := strconv.FormatUint(msgGetUint(entry, 2), 10)

	return Conversation{
		ID:           convID,
		Participants: []string{userID},
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
// GetMessages — stub (not yet implemented)
// ---------------------------------------------------------------------------

func (c *Client) GetMessages(ctx context.Context, convID string, cursor string) ([]Message, error) {
	return nil, fmt.Errorf("GetMessages: not yet implemented")
}
