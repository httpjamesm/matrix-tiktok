package libtiktok

import (
	"context"
	"encoding/json"
	"fmt"
)

type User struct {
	ID        string
	UniqueID  string
	Nickname  string
	AvatarURL string
}

type userProfileResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
	Users      []struct {
		IMUserProfile struct {
			UserIDStr string `json:"user_id_str"`
			UniqueID  string `json:"unique_id"`
			NickName  string `json:"nick_name"`
			Avatars   struct {
				AvatarMedium struct {
					URLList []string `json:"url_list"`
				} `json:"avatar_medium"`
			} `json:"avatars"`
		} `json:"im_user_profile"`
	} `json:"users"`
}

func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	idsJSON, err := json.Marshal([]string{userID})
	if err != nil {
		return nil, fmt.Errorf("marshal user_ids: %w", err)
	}

	resp, err := c.r.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"aid":      "1988",
			"user_ids": string(idsJSON),
		}).
		Get("/tiktok/v1/im/user/profile/")
	if err != nil {
		return nil, fmt.Errorf("get user profile: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("user profile API returned %d: %s", resp.StatusCode(), resp.String())
	}

	var result userProfileResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse user profile response: %w", err)
	}
	if result.StatusCode != 0 {
		return nil, fmt.Errorf("user profile API error %d: %s", result.StatusCode, result.StatusMsg)
	}
	if len(result.Users) == 0 {
		return nil, fmt.Errorf("user %q not found", userID)
	}

	p := result.Users[0].IMUserProfile
	u := &User{
		ID:       p.UserIDStr,
		UniqueID: p.UniqueID,
		Nickname: p.NickName,
	}
	if len(p.Avatars.AvatarMedium.URLList) > 0 {
		u.AvatarURL = p.Avatars.AvatarMedium.URLList[0]
	}

	return u, nil
}
