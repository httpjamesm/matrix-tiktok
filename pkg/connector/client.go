package connector

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// tiktokMatrixMediaMaxBytes is advertised in com.beeper.room_features for media uploads
// and is a soft cap for clients; TikTok may still reject oversized payloads.
const tiktokMatrixMediaMaxBytes = 50 * 1024 * 1024

const typingHeartbeatTimeout = 5 * time.Second

type typingStateKey struct {
	ConversationID string
	SenderUserID   string
}

type typingTimerState struct {
	seq   uint64
	timer *time.Timer
}

// TikTokClient implements bridgev2.NetworkAPI for a single logged-in TikTok session.
type TikTokClient struct {
	connector *TikTokConnector
	userLogin *bridgev2.UserLogin
	meta      *UserLoginMetadata
	apiClient *libtiktok.Client

	stopLoop    context.CancelFunc
	isConnected bool

	// Coalesces concurrent Connect calls (e.g. provisioning + startup) so two
	// overlapping GetSelf sequences cannot leave BAD_CREDENTIALS while another
	// run succeeds and starts backfill.
	connectFlight singleflight.Group

	// In-memory state — reset on restart, but the bridge deduplicates by message ID.
	mu         sync.Mutex
	lastSeen   map[string]int64  // convID → highest dispatched message timestamp (ms)
	otherUsers map[string]string // convID → other participant's TikTok user ID
	groupNames map[string]string // convID → explicit TikTok group title
	typing     map[typingStateKey]*typingTimerState

	typingTimeout           time.Duration
	queueRemoteEventForTest func(bridgev2.RemoteEvent)
}

// newTikTokClient is the canonical constructor used by both LoadUserLogin and
// TikTokLogin.finishLogin.
func newTikTokClient(connector *TikTokConnector, userLogin *bridgev2.UserLogin, meta *UserLoginMetadata) *TikTokClient {
	return &TikTokClient{
		connector:  connector,
		userLogin:  userLogin,
		meta:       meta,
		apiClient:  libtiktok.NewClient(meta.Cookies),
		lastSeen:   make(map[string]int64),
		otherUsers: make(map[string]string),
		groupNames: make(map[string]string),
		typing:     make(map[typingStateKey]*typingTimerState),

		typingTimeout: typingHeartbeatTimeout,
	}
}

func userLocalPortalInfoFromMuted(muted *bool) *bridgev2.UserLocalPortalInfo {
	if muted == nil {
		return nil
	}
	mutedUntil := bridgev2.Unmuted
	if *muted {
		mutedUntil = event.MutedForever
	}
	return &bridgev2.UserLocalPortalInfo{
		MutedUntil: &mutedUntil,
	}
}

func (tc *TikTokClient) applyPortalMuteState(ctx context.Context, portal *bridgev2.Portal, muted *bool) {
	if portal == nil || portal.MXID == "" || muted == nil || tc.userLogin == nil || tc.userLogin.User == nil {
		return
	}
	dp := tc.userLogin.User.DoublePuppet(ctx)
	if dp == nil {
		zerolog.Ctx(ctx).Debug().
			Str("portal_id", string(portal.ID)).
			Msg("Skipping TikTok mute sync: no double puppet available")
		return
	}

	mutedUntil := bridgev2.Unmuted
	if *muted {
		mutedUntil = event.MutedForever
	}
	if err := dp.MuteRoom(ctx, portal.MXID, mutedUntil); err != nil {
		zerolog.Ctx(ctx).Err(err).
			Str("portal_id", string(portal.ID)).
			Bool("muted", *muted).
			Msg("Failed to sync TikTok mute state to Matrix")
	}
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Ensure TikTokClient fully implements the required interfaces.
var _ bridgev2.NetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.IdentifierResolvingNetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.ReactionHandlingNetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.RedactionHandlingNetworkAPI = (*TikTokClient)(nil)

func ptrInt(v int) *int { return &v }
