package connector

import (
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// makeUserID creates a networkid.UserID from a TikTok numeric user ID string.
// TikTok user IDs are stable numeric identifiers that persist across username changes.
func makeUserID(tiktokUserID string) networkid.UserID {
	return networkid.UserID(tiktokUserID)
}

// makePortalID creates a networkid.PortalID from a TikTok conversation ID.
// For DMs this is typically the other participant's user ID; for group chats
// it is the conversation/room ID returned by the TikTok API.
func makePortalID(conversationID string) networkid.PortalID {
	return networkid.PortalID(conversationID)
}

// makeUserLoginID creates a networkid.UserLoginID from a TikTok user ID.
// We use the same value as UserID because a single TikTok account maps 1-to-1
// with a bridge login.
func makeUserLoginID(tiktokUserID string) networkid.UserLoginID {
	return networkid.UserLoginID(tiktokUserID)
}
