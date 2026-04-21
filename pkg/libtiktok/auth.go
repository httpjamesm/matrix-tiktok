package libtiktok

import (
	"context"
	"fmt"
	"time"
)

type Self struct {
	UserID    string
	UniqueID  string // the @username
	Nickname  string // display name
	AvatarURL string
}

func (c *Client) GetSelf(ctx context.Context) (*Self, error) {
	data, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, fmt.Errorf("get universal data: %w", err)
	}

	appContext, err := data.getAppContext()
	if err != nil {
		return nil, fmt.Errorf("failed to get appContext: %w", err)
	}

	user, ok := appContext["user"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("user not found or wrong type")
	}

	uid, _ := user["uid"].(string)
	uniqueID, _ := user["uniqueId"].(string)
	nickname, _ := user["nickName"].(string)
	if uid == "" || uniqueID == "" {
		return nil, fmt.Errorf("user fields missing uid or uniqueId")
	}

	var avatarURL string
	if avatarURIs, ok := user["avatarUri"].([]any); ok && len(avatarURIs) > 0 {
		avatarURL, _ = avatarURIs[0].(string)
	}

	return &Self{
		UserID:    uid,
		UniqueID:  uniqueID,
		Nickname:  nickname,
		AvatarURL: avatarURL,
	}, nil
}

// GetSelfWithRetry calls GetSelf up to maxAttempts times with bounded
// exponential backoff between tries. TikTok's /messages page sometimes
// returns HTML without usable hydration data on back-to-back requests, which
// is especially visible right after an interactive login validates the
// session and Connect immediately re-validates.
func (c *Client) GetSelfWithRetry(ctx context.Context, maxAttempts int) (*Self, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(150*(1<<uint(attempt-1))) * time.Millisecond
			const maxDelay = 2 * time.Second
			if delay > maxDelay {
				delay = maxDelay
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		self, err := c.GetSelf(ctx)
		if err == nil {
			return self, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("get self failed after %d attempts: %w", maxAttempts, lastErr)
}
