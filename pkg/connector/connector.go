package connector

import (
	"context"
	_ "embed"
	"fmt"

	"go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

// TikTokConnector is the main entrypoint for the TikTok bridge connector.
// It implements bridgev2.NetworkConnector and is passed to the central
// mautrix bridge module on startup.
type TikTokConnector struct {
	br     *bridgev2.Bridge
	Config Config
}

// Compile-time assertion that *TikTokConnector implements bridgev2.NetworkConnector.
var _ bridgev2.NetworkConnector = (*TikTokConnector)(nil)

// Init is called during bridge initialisation. It stores the bridge reference
// and should not perform any IO.
func (tc *TikTokConnector) Init(bridge *bridgev2.Bridge) {
	tc.br = bridge
}

// Start is called after Init, once the bridge is ready to perform IO.
// TikTok uses polling/websockets rather than inbound webhooks, so there is
// nothing to register here. Bridge-wide database migrations would also go here.
func (tc *TikTokConnector) Start(_ context.Context) error {
	return nil
}

// GetCapabilities returns bridge-wide capability flags.
func (tc *TikTokConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		// Set to true once disappearing-message support is wired up.
		DisappearingMessages: false,
	}
}

// GetBridgeInfoVersion returns version counters that tell the bridge when to
// resend room-info / capability state events.  Increment the first value when
// uk.half-shot.bridge changes, and the second when com.beeper.room_features changes.
func (tc *TikTokConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 5 // bump capabilities when com.beeper.room_features changes (e.g. m.video support)
}

// GetName returns static metadata that identifies this bridge to Matrix.
func (tc *TikTokConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName: "TikTok",
		NetworkURL:  "https://www.tiktok.com",
		// TODO: upload a real TikTok icon to a Matrix server and replace this URI.
		NetworkIcon:      "mxc://maunium.net/TODO",
		NetworkID:        "tiktok",
		BeeperBridgeType: "github.com/httpjamesm/matrix-tiktok",
		// Port chosen to avoid conflicts; register at https://mau.fi/ports if needed.
		DefaultPort:          29380,
		DefaultCommandPrefix: "!tt",
	}
}

// -----------------------------------------------------------------------
// Config
// -----------------------------------------------------------------------

// Config holds TikTok-specific configuration values that live under the
// `network:` key in config.yaml.
type Config struct {
	// How often to poll TikTok for new messages.
	PollIntervalSeconds int `yaml:"poll_interval_seconds"`
	// Optional override for the TikTok API base URL.
	APIBaseURL string `yaml:"api_base_url"`
	// Optional override for the TikTok HTTP User-Agent.
	UserAgent string `yaml:"user_agent"`
	// Maximum REST history pages to fetch per conversation on connect.
	// Zero uses the built-in default.
	InitialBackfillMaxPages int `yaml:"initial_backfill_max_pages"`
	// Maximum number of inbox conversations to history-fetch on connect.
	// Zero means all inbox conversations.
	InitialBackfillMaxConversations int `yaml:"initial_backfill_max_conversations"`
	// How far back startup backfill should look for conversations without a
	// stored checkpoint. Zero uses the built-in default.
	InitialBackfillLookbackHours int `yaml:"initial_backfill_lookback_hours"`
}

//go:embed example-config.yaml
var ExampleConfig string

func upgradeConfig(helper configupgrade.Helper) {
	// Copy config fields across upgrades here.
	// e.g.: helper.Copy(configupgrade.Str, "device_id")
}

// GetConfig returns the example config string, a pointer to the config struct
// that YAML will be decoded into, and the upgrade helper.
func (tc *TikTokConnector) GetConfig() (example string, data any, upgrader configupgrade.Upgrader) {
	return ExampleConfig, &tc.Config, configupgrade.SimpleUpgrader(upgradeConfig)
}

// -----------------------------------------------------------------------
// Database metadata types
// -----------------------------------------------------------------------

// GetDBMetaTypes tells the bridge which Go types to use for the JSON metadata
// columns in each database table.
func (tc *TikTokConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: func() any {
			return &PortalMetadata{}
		},
		Ghost: nil,
		Message: func() any {
			return &MessageMetadata{}
		},
		Reaction: nil,
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}

// UserLoginMetadata stores the TikTok session credentials for a single login.
type UserLoginMetadata struct {
	// TikTok numeric user ID (stable across username changes).
	UserID string `json:"user_id"`
	// TikTok @username at the time of login (display only).
	Username string `json:"username"`
	// Cookies is the raw Cookie header string copied from an authenticated
	// TikTok in-app or browser session (e.g. "sessionid=abc123; tt_csrf_token=xyz; …").
	// The bridge sends this verbatim on every API request to impersonate the browser.
	Cookies string `json:"cookies"`
}

// PortalMetadata stores TikTok-specific data alongside each bridged room.
type PortalMetadata struct {
	// The TikTok conversation / inbox thread ID.
	ConversationID string `json:"conversation_id,omitempty"`
	// SourceID is the extra TikTok conversation identifier required by follow-up
	// APIs like history fetch and message reactions.
	SourceID uint64 `json:"source_id,omitempty"`
	// GroupName is the explicit TikTok group title when one exists.
	GroupName string `json:"group_name,omitempty"`
	// ConversationType is the wire conversation_type value: 1 for DMs, 2 for group chats.
	// Used to set message_kind correctly when sending outbound messages.
	ConversationType uint64 `json:"conversation_type,omitempty"`
	// Muted caches the per-user TikTok mute state derived from get_by_user_init field 51 metadata.
	Muted *bool `json:"muted,omitempty"`
	// LastSeenMessageTimestampMs is the highest TikTok message timestamp that was
	// queued during startup backfill.
	LastSeenMessageTimestampMs int64 `json:"last_seen_message_timestamp_ms,omitempty"`
	// LastSeenMessageID is the server message id paired with LastSeenMessageTimestampMs.
	LastSeenMessageID uint64 `json:"last_seen_message_id,omitempty"`
	// LastSeenCursorTsUs is the wire cursor_ts_us of the newest queued message,
	// retained as extra resume/debug context for future backfill tuning.
	LastSeenCursorTsUs uint64 `json:"last_seen_cursor_ts_us,omitempty"`
}

// MessageMetadata stores TikTok-specific data alongside each bridged message.
type MessageMetadata struct {
	// Original TikTok message type (e.g. "text", "image", "video", "sticker").
	MsgType string `json:"msg_type,omitempty"`
	// SendChainID is TikTok inner wire field 5; copied to send body field 3 for aweType 703 replies.
	SendChainID uint64 `json:"send_chain_id,omitempty"`
	// SenderSecUID is the message sender's sec_uid (wire field 14).
	SenderSecUID string `json:"sender_sec_uid,omitempty"`
	// CursorTsUs is wire field 25 (µs); used as parent_cursor_ts_us when replying from Matrix.
	CursorTsUs uint64 `json:"cursor_ts_us,omitempty"`
	// ContentJSON is the raw field-8 JSON body from TikTok (for refmsg content on outbound replies).
	ContentJSON string `json:"content_json,omitempty"`
}

// CopyFrom merges non-zero fields from another MessageMetadata (used when merging media+caption parts).
func (m *MessageMetadata) CopyFrom(other any) {
	o, ok := other.(*MessageMetadata)
	if !ok || o == nil || m == nil {
		return
	}
	if o.MsgType != "" {
		m.MsgType = o.MsgType
	}
	if o.SendChainID != 0 {
		m.SendChainID = o.SendChainID
	}
	if o.SenderSecUID != "" {
		m.SenderSecUID = o.SenderSecUID
	}
	if o.CursorTsUs != 0 {
		m.CursorTsUs = o.CursorTsUs
	}
	if o.ContentJSON != "" {
		m.ContentJSON = o.ContentJSON
	}
}

// -----------------------------------------------------------------------
// LoadUserLogin
// -----------------------------------------------------------------------

// LoadUserLogin prepares an existing login for connection by attaching a
// TikTokClient to the UserLogin.  Actual network connectivity happens later
// in TikTokClient.Connect.
func (tc *TikTokConnector) LoadUserLogin(_ context.Context, login *bridgev2.UserLogin) error {
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok {
		return fmt.Errorf("unexpected metadata type %T for user login %s", login.Metadata, login.ID)
	}
	if login.Client != nil {
		login.Client.Disconnect()
	}
	login.Client = newTikTokClient(tc, login, meta)
	return nil
}

// -----------------------------------------------------------------------
// Login flows
// -----------------------------------------------------------------------

const loginFlowIDSession = "session-cookie"

// GetLoginFlows returns the available ways to log into TikTok.
func (tc *TikTokConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			ID:          loginFlowIDSession,
			Name:        "Session cookie",
			Description: "Log in by pasting the full Cookie header string from an authenticated TikTok browser or in-app browser session.",
		},
	}
}

// CreateLogin instantiates a LoginProcess for the chosen flow.
func (tc *TikTokConnector) CreateLogin(_ context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != loginFlowIDSession {
		return nil, fmt.Errorf("unknown login flow %q", flowID)
	}
	return &TikTokLogin{
		User:      user,
		Connector: tc,
	}, nil
}
