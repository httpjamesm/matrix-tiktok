package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// GetUserInfo fetches live profile data for a TikTok ghost user.
func (tc *TikTokClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	user, err := tc.apiClient.GetUser(ctx, string(ghost.ID))
	if err != nil {
		return nil, fmt.Errorf("get user info for %s: %w", ghost.ID, err)
	}

	name := user.Nickname
	if name == "" {
		name = "@" + user.UniqueID
	}

	return &bridgev2.UserInfo{
		Name:        &name,
		Identifiers: []string{fmt.Sprintf("tiktok:@%s", user.UniqueID)},
		Avatar:      tc.makeGhostAvatar(user),
	}, nil
}

// makeGhostAvatar builds a bridgev2.Avatar from a TikTok user profile.
// When AvatarURL is empty the returned value signals removal so that a
// previously set avatar is cleared on the Matrix side.
func (tc *TikTokClient) makeGhostAvatar(user *libtiktok.User) *bridgev2.Avatar {
	if user.AvatarURL == "" {
		return &bridgev2.Avatar{
			ID:     "remove",
			Remove: true,
		}
	}
	avatarURL := user.AvatarURL
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(avatarURL),
		Get: func(ctx context.Context) ([]byte, error) {
			return tc.apiClient.DownloadAvatar(ctx, avatarURL)
		},
	}
}

// fetchGhostAvatar fetches the latest avatar for ghost from TikTok and
// applies it via ghost.UpdateAvatar. It returns true when the avatar was
// actually changed, matching the convention used by other mautrix bridges.
func (tc *TikTokClient) fetchGhostAvatar(ctx context.Context, ghost *bridgev2.Ghost) bool {
	user, err := tc.apiClient.GetUser(ctx, string(ghost.ID))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).
			Str("ghost_id", string(ghost.ID)).
			Msg("Failed to get user info for avatar update")
		return false
	}
	return ghost.UpdateAvatar(ctx, tc.makeGhostAvatar(user))
}

// ResolveIdentifier will enable the `start-chat` bot command once
// libtiktok.GetUserByUsername is implemented.
func (tc *TikTokClient) ResolveIdentifier(_ context.Context, identifier string, _ bool) (*bridgev2.ResolveIdentifierResponse, error) {
	return nil, fmt.Errorf("start-chat not yet available: GetUserByUsername is not implemented (got %q)", identifier)
}
