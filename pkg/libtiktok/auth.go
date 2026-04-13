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
	data, err := c.GetMessages()
	if err != nil {
		return nil, fmt.Errorf("get universal data: %w", err)
	}

	defaultScope, ok := data["__DEFAULT_SCOPE__"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("__DEFAULT_SCOPE__ not found or wrong type")
	}

	appContext, ok := defaultScope["webapp.app-context"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("webapp.app-context not found or wrong type")
	}

	user, ok := appContext["user"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("user not found or wrong type")
	}

	uid, _ := user["uid"].(string)
	uniqueID, _ := user["uniqueId"].(string)
	nickname, _ := user["nickName"].(string)

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
