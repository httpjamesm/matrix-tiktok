package libtiktok

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	sendMsgPath    = "/v1/message/send"
	sendMsgFullURL = "https://im-api-sg.tiktok.com/v1/message/send"

	setPropertyPath    = "/v1/message/set_property"
	setPropertyFullURL = "https://im-api-sg.tiktok.com/v1/message/set_property"
)

// SendMessageParams holds the parameters for sending a message.
type SendMessageParams struct {
	ConvID string
	Text   string
	// later: MediaData []byte, MimeType string, etc.
}

// SendMessageResponse holds the result of a successful SendMessage call.
type SendMessageResponse struct {
	MessageID string
}

// ---------------------------------------------------------------------------
// EC key helpers
// ---------------------------------------------------------------------------

// generateP256Key generates a fresh secp256r1 (P-256) ECDSA keypair.
func generateP256Key() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// p256UncompressedPoint returns the 65-byte uncompressed public key point
// 0x04 || x || y, each coordinate zero-padded to 32 bytes.
func p256UncompressedPoint(priv *ecdsa.PrivateKey) []byte {
	xRaw := priv.PublicKey.X.Bytes()
	yRaw := priv.PublicKey.Y.Bytes()
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	copy(xBytes[32-len(xRaw):], xRaw)
	copy(yBytes[32-len(yRaw):], yRaw)
	out := make([]byte, 65)
	out[0] = 0x04
	copy(out[1:33], xBytes)
	copy(out[33:65], yBytes)
	return out
}

// padTo32 zero-pads raw big-endian bytes to exactly 32 bytes.
// Used for P-256 coordinate and signature component encoding.
func padTo32(raw []byte) []byte {
	if len(raw) >= 32 {
		return raw[len(raw)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out
}

// b64url returns the standard base64url (no-padding) encoding of b.
func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// DPoP JWT (RFC 9449-style, ES256)
// ---------------------------------------------------------------------------

// buildDPoPJWT creates a DPoP proof JWT signed with ES256 (ECDSA P-256 + SHA-256).
// The public JWK is embedded in the JWT header so the server can verify without a
// pre-registered key. htm is the HTTP method and htu is the absolute request URL.
func buildDPoPJWT(priv *ecdsa.PrivateKey, htm, htu string) (string, error) {
	xBytes := padTo32(priv.PublicKey.X.Bytes())
	yBytes := padTo32(priv.PublicKey.Y.Bytes())

	// Public key as a JWK embedded in the JWT header (P-256 / ES256).
	jwk := map[string]string{
		"crv": "P-256",
		"kty": "EC",
		"x":   b64url(xBytes),
		"y":   b64url(yBytes),
	}
	headerMap := map[string]any{
		"alg": "ES256",
		"typ": "dpop+jwt",
		"jwk": jwk,
	}
	headerJSON, err := json.Marshal(headerMap)
	if err != nil {
		return "", fmt.Errorf("marshal DPoP header: %w", err)
	}

	jtiRaw := make([]byte, 32)
	if _, err := rand.Read(jtiRaw); err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}
	payloadMap := map[string]any{
		"jti": b64url(jtiRaw),
		"htm": htm,
		"htu": htu,
		"iat": time.Now().Unix(),
	}
	payloadJSON, err := json.Marshal(payloadMap)
	if err != nil {
		return "", fmt.Errorf("marshal DPoP payload: %w", err)
	}

	signingInput := b64url(headerJSON) + "." + b64url(payloadJSON)
	h := sha256.Sum256([]byte(signingInput))

	// Sign and produce a raw r||s signature (IEEE P1363 / JWS ES256 format).
	r, s, err := ecdsa.Sign(rand.Reader, priv, h[:])
	if err != nil {
		return "", fmt.Errorf("sign DPoP JWT: %w", err)
	}
	sig := make([]byte, 64)
	rRaw := r.Bytes()
	sRaw := s.Bytes()
	copy(sig[32-len(rRaw):32], rRaw)
	copy(sig[64-len(sRaw):64], sRaw)

	return signingInput + "." + b64url(sig), nil
}

// ---------------------------------------------------------------------------
// Metadata builder for the send endpoint
// ---------------------------------------------------------------------------

// buildSendMetadata returns the metadata key-value pairs for the send endpoint.
// It reuses buildMetadata from inbox.go and inserts the five tt-ticket-guard
// fields immediately before the trailing browser_version entry, matching the
// order the web client uses.
func buildSendMetadata(deviceID, msToken, verifyFP, publicKeyB64 string) []metaKV {
	base := buildMetadata(deviceID, msToken, verifyFP)

	// base always ends with browser_version; save it, trim it, then reattach
	// after the ticket-guard entries so the ordering matches the web client.
	last := base[len(base)-1]
	pairs := make([]metaKV, len(base)-1, len(base)+5)
	copy(pairs, base[:len(base)-1])

	pairs = append(pairs,
		metaKV{"tt-ticket-guard-public-key", publicKeyB64},
		metaKV{"tt-ticket-guard-client-data", ""},
		metaKV{"tt-ticket-guard-version", "2"},
		metaKV{"tt-ticket-guard-iteration-version", "0"},
		metaKV{"tt-ticket-guard-web-version", "1"},
		last, // browser_version goes last
	)
	return pairs
}

// ---------------------------------------------------------------------------
// Protobuf payload
// ---------------------------------------------------------------------------

// buildSendPayload constructs the protobuf request body for POST /v1/message/send.
// The structure mirrors the send.json typedef observed in the TikTok web client:
//
//	top-level (type 100 / sub-cmd 10007)
//	  └─ field 8 → { field 100 → inner chat message }
//	       field 1  conversation ID
//	       field 2  msg type (1 = text)
//	       field 3  0
//	       field 4  JSON body {"aweType":0,"text":"..."}
//	       field 5  s:mentioned_users   (repeated, first occurrence)
//	       field 5  s:client_message_id (repeated, second occurrence)
//	       field 6  7
//	       field 7  "deprecated"
//	       field 8  client message UUID
//	  └─ field 15  repeated metadata k/v pairs (including ticket-guard)
func buildSendPayload(convID, text, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID string) []byte {
	// Encode the message text in the JSON envelope TikTok expects.
	textJSON, _ := json.Marshal(map[string]any{"aweType": 0, "text": text})

	// Field 5 (first occurrence): s:mentioned_users → empty sub-message value.
	mentionedUsersKV := func() []byte {
		var p []byte
		p = append(p, pbStringField(1, "s:mentioned_users")...)
		p = append(p, pbEmbedField(2, nil)...)
		return p
	}()

	// Field 5 (second occurrence, repeated): s:client_message_id → UUID string.
	clientMsgIDKV := func() []byte {
		var p []byte
		p = append(p, pbStringField(1, "s:client_message_id")...)
		p = append(p, pbStringField(2, clientMsgID)...)
		return p
	}()

	// Inner chat message (lives at field 8 → field 100 in the outer envelope).
	var inner []byte
	inner = append(inner, pbStringField(1, convID)...)
	inner = append(inner, pbVarintField(2, 1)...)
	inner = append(inner, pbVarintField(3, 0)...)
	inner = append(inner, pbBytesField(4, textJSON)...)
	inner = append(inner, pbEmbedField(5, mentionedUsersKV)...) // first occurrence
	inner = append(inner, pbEmbedField(5, clientMsgIDKV)...)    // second occurrence (repeated)
	inner = append(inner, pbVarintField(6, 7)...)
	inner = append(inner, pbStringField(7, "deprecated")...)
	inner = append(inner, pbStringField(8, clientMsgID)...)

	// Field 8 outer wrapper: { 100: inner }.
	field8Content := pbEmbedField(100, inner)

	// Field 15: repeated embedded metadata key-value pairs.
	var metaBytes []byte
	for _, kv := range buildSendMetadata(deviceID, msToken, verifyFP, publicKeyB64) {
		var pair []byte
		pair = append(pair, pbStringField(1, kv.k)...)
		pair = append(pair, pbStringField(2, kv.v)...)
		metaBytes = append(metaBytes, pbEmbedField(15, pair)...)
	}

	// Top-level envelope — same wire shape as inbox/get_by_conversation but with
	// message type 100 and sub-command 10007.
	var msg []byte
	msg = append(msg, pbVarintField(1, 100)...)     // message type: send
	msg = append(msg, pbVarintField(2, 10007)...)   // sub-command
	msg = append(msg, pbStringField(3, "1.6.0")...) // client version
	msg = append(msg, pbEmbedField(4, nil)...)      // empty options message
	msg = append(msg, pbVarintField(5, 3)...)       // platform flag: web_pc
	msg = append(msg, pbVarintField(6, 0)...)
	msg = append(msg, pbStringField(7, "")...)
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

// parseSendResponse attempts to extract the server-assigned message ID from the
// send response protobuf.
//
// The response envelope mirrors the inbox/get_by_conversation shape: the inner
// payload sits at field 6, which in turn nests the echoed message at field 100
// → field 1 (message ID string). A second probe at field 8 → 100 → 1 is tried
// as a fallback.
//
// Returns an error when no ID can be located; the caller falls back to the
// client-generated UUID in that case.
func parseSendResponse(body []byte) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("empty response body")
	}
	top, err := pbDecode(body)
	if err != nil {
		return "", fmt.Errorf("decode send response: %w", err)
	}

	// Primary path: field 6 → field 100 → field 1.
	if f6Raw := msgGetBytes(top, 6); len(f6Raw) > 0 {
		if f6, err := pbDecode(f6Raw); err == nil {
			if f100Raw := msgGetBytes(f6, 100); len(f100Raw) > 0 {
				if f100, err := pbDecode(f100Raw); err == nil {
					if id := msgGetString(f100, 1); id != "" {
						return id, nil
					}
				}
			}
		}
	}

	// Fallback: field 8 → field 100 → field 1.
	if f8Raw := msgGetBytes(top, 8); len(f8Raw) > 0 {
		if f8, err := pbDecode(f8Raw); err == nil {
			if f100Raw := msgGetBytes(f8, 100); len(f100Raw) > 0 {
				if f100, err := pbDecode(f100Raw); err == nil {
					if id := msgGetString(f100, 1); id != "" {
						return id, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("server-assigned message ID not found in response")
}

// ---------------------------------------------------------------------------
// SendMessage
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Reaction types
// ---------------------------------------------------------------------------

// ReactionAction indicates whether to add or remove a reaction.
type ReactionAction uint64

const (
	// ReactionAdd adds the emoji reaction to a message.
	ReactionAdd ReactionAction = 0
	// ReactionRemove removes the emoji reaction from a message.
	ReactionRemove ReactionAction = 1
)

// SendReactionParams holds the parameters for reacting to a message.
type SendReactionParams struct {
	// ConvID is the conversation ID (e.g. "0:1:X:Y").
	ConvID string
	// Emoji is the raw emoji character to react with; the "e:" prefix is added
	// internally. Example: "❤️"
	Emoji string
	// Action is ReactionAdd or ReactionRemove.
	Action ReactionAction
	// SelfUserID is the numeric user ID of the person sending the reaction.
	SelfUserID      string
	ConvoSourceID   uint64
	ServerMessageID uint64
}

// ---------------------------------------------------------------------------
// Reaction protobuf payload
// ---------------------------------------------------------------------------

// buildReactionPayload constructs the protobuf request body for
// POST /v1/message/set_property.
//
// Wire shape (field numbers mirror the captured typedef):
//
//	top-level (type 705 / sub-cmd 10008)
//	  └─ field 8 → { field 705 → reaction wrapper }
//	       field 1  → inner reaction message
//	           field 1  conversation ID
//	           field 2  1 (action flag)
//	           field 3  ConvoSourceID
//	           field 4  ServerMessageID
//	           field 5  s:client_message_id UUID
//	           field 6  repeated reaction entry { 1:action  2:"e:<emoji>"  4:userID }
//	       field 2  "deprecated"
//	  └─ field 15  repeated metadata k/v pairs (including ticket-guard)
func buildReactionPayload(p SendReactionParams, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID string) []byte {
	// Reaction entry: {1: action, 2: "e:<emoji>", 4: userID}
	var reactionEntry []byte
	reactionEntry = append(reactionEntry, pbVarintField(1, uint64(p.Action))...)
	reactionEntry = append(reactionEntry, pbStringField(2, "e:"+p.Emoji)...)
	reactionEntry = append(reactionEntry, pbStringField(4, p.SelfUserID)...)

	// Inner reaction message (8→705→1).
	var inner []byte
	inner = append(inner, pbStringField(1, p.ConvID)...)
	inner = append(inner, pbVarintField(2, 1)...)
	inner = append(inner, pbVarintField(3, p.ConvoSourceID)...)
	inner = append(inner, pbVarintField(4, p.ServerMessageID)...)

	inner = append(inner, pbStringField(5, clientMsgID)...)
	inner = append(inner, pbEmbedField(6, reactionEntry)...)

	// Reaction wrapper (8→705): { 1: inner, 2: "deprecated" }
	var wrapper []byte
	wrapper = append(wrapper, pbEmbedField(1, inner)...)
	wrapper = append(wrapper, pbStringField(2, "deprecated")...)

	// Field 8: { 705: wrapper }
	field8Content := pbEmbedField(705, wrapper)

	// Metadata key-value pairs (field 15, repeated).
	var metaBytes []byte
	for _, kv := range buildSendMetadata(deviceID, msToken, verifyFP, publicKeyB64) {
		var pair []byte
		pair = append(pair, pbStringField(1, kv.k)...)
		pair = append(pair, pbStringField(2, kv.v)...)
		metaBytes = append(metaBytes, pbEmbedField(15, pair)...)
	}

	// Top-level envelope — message type 705, sub-command 10008.
	var msg []byte
	msg = append(msg, pbVarintField(1, 705)...)
	msg = append(msg, pbVarintField(2, 10008)...)
	msg = append(msg, pbStringField(3, "1.6.0")...)
	msg = append(msg, pbEmbedField(4, nil)...)
	msg = append(msg, pbVarintField(5, 3)...)
	msg = append(msg, pbVarintField(6, 0)...)
	msg = append(msg, pbStringField(7, "")...)
	msg = append(msg, pbEmbedField(8, field8Content)...)
	msg = append(msg, pbStringField(9, deviceID)...)
	msg = append(msg, pbStringField(11, "web")...)
	msg = append(msg, metaBytes...)
	msg = append(msg, pbVarintField(18, 1)...)
	return msg
}

// ---------------------------------------------------------------------------
// SendReaction
// ---------------------------------------------------------------------------

// SendReaction adds or removes an emoji reaction on a message.
//
// Protocol summary:
//  1. Fetch the device ID (wid) from /messages universal data.
//  2. Generate a fresh P-256 keypair for tt-ticket-guard.
//  3. Build a DPoP proof JWT (ES256) bound to POST setPropertyFullURL.
//  4. Construct the type-705 protobuf request body.
//  5. POST to /v1/message/set_property with ztca-dpop in the query string.
func (c *Client) SendReaction(ctx context.Context, p SendReactionParams) error {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	priv, err := generateP256Key()
	if err != nil {
		return fmt.Errorf("generate P-256 key: %w", err)
	}
	publicKeyB64 := base64.StdEncoding.EncodeToString(p256UncompressedPoint(priv))

	dpopToken, err := buildDPoPJWT(priv, "POST", setPropertyFullURL)
	if err != nil {
		return fmt.Errorf("build DPoP JWT: %w", err)
	}

	clientMsgID := uuid.New().String()

	payload := buildReactionPayload(p, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID)

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Pragma", "no-cache").
		SetHeader("Priority", "u=1, i").
		SetHeader("tt-ticket-guard-iteration-version", "0").
		SetHeader("tt-ticket-guard-public-key", publicKeyB64).
		SetHeader("tt-ticket-guard-version", "2").
		SetHeader("tt-ticket-guard-web-version", "1").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"ztca-version":    "1",
			"ztca-dpop":       dpopToken,
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(setPropertyPath)
	if err != nil {
		return fmt.Errorf("POST set_property: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("set_property API returned %d: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// SendMessage sends a text message to the specified conversation and returns the
// acknowledged message ID.
//
// Protocol summary (matching the TikTok web IM client):
//  1. Fetch the device ID (wid) from /messages universal data.
//  2. Generate a fresh P-256 keypair — the uncompressed public key is embedded
//     both in the protobuf metadata (tt-ticket-guard-public-key) and the HTTP
//     request header.
//  3. Build a DPoP proof JWT (ES256) bound to POST sendMsgFullURL.
//  4. Construct the type-100 protobuf request body.
//  5. POST to /v1/message/send with ztca-dpop in the query string and all
//     ticket-guard headers set.
//  6. Parse the response for a server-assigned message ID; fall back to the
//     client-generated UUID if the response structure is opaque.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (*SendMessageResponse, error) {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")

	// Generate a fresh P-256 keypair for tt-ticket-guard authentication.
	priv, err := generateP256Key()
	if err != nil {
		return nil, fmt.Errorf("generate P-256 key: %w", err)
	}
	publicKeyB64 := base64.StdEncoding.EncodeToString(p256UncompressedPoint(priv))

	// Build a DPoP proof JWT bound to this specific POST request.
	dpopToken, err := buildDPoPJWT(priv, "POST", sendMsgFullURL)
	if err != nil {
		return nil, fmt.Errorf("build DPoP JWT: %w", err)
	}

	// Client-generated UUID v4, echoed inside the protobuf as s:client_message_id
	// and field 8 of the inner message.
	clientMsgID := uuid.New().String()

	payload := buildSendPayload(p.ConvID, p.Text, deviceID, msToken, verifyFP, publicKeyB64, clientMsgID)

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Pragma", "no-cache").
		SetHeader("Priority", "u=1, i").
		SetHeader("tt-ticket-guard-iteration-version", "0").
		SetHeader("tt-ticket-guard-public-key", publicKeyB64).
		SetHeader("tt-ticket-guard-version", "2").
		SetHeader("tt-ticket-guard-web-version", "1").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"ztca-version":    "1",
			"ztca-dpop":       dpopToken,
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(sendMsgPath)
	if err != nil {
		return nil, fmt.Errorf("POST send message: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("send API returned %d: %s", resp.StatusCode(), resp.String())
	}

	msgID, err := parseSendResponse(resp.Body())
	if err != nil {
		// The server response format isn't fully documented; fall back to the
		// client-generated ID so the caller always receives a non-empty value.
		msgID = clientMsgID
	}

	return &SendMessageResponse{MessageID: msgID}, nil
}
