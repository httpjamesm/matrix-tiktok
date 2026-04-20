package libtiktok

import (
	"context"
	"fmt"
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
