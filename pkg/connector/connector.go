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
	return 1, 2 // bump capabilities when com.beeper.room_features changes
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
	// TODO: add TikTok-specific fields as the PoC connector is integrated.
	// Example:
	//   DeviceID    string `yaml:"device_id"`
	//   Proxy       string `yaml:"proxy"`
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
}

// MessageMetadata stores TikTok-specific data alongside each bridged message.
type MessageMetadata struct {
	// Original TikTok message type (e.g. "text", "image", "video", "sticker").
	MsgType string `json:"msg_type,omitempty"`
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
