package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

// Ensure TikTokLogin implements the required login process interfaces.
var _ bridgev2.LoginProcessUserInput = (*TikTokLogin)(nil)

// TikTokLogin holds state across the multi-step login process.
type TikTokLogin struct {
	// User is the Matrix user initiating the login.
	User *bridgev2.User
	// Connector is the parent connector, needed to call the TikTok API.
	Connector *TikTokConnector

	// cookies is the raw browser Cookie header string captured from the first
	// input step, used to impersonate the authenticated browser session.
	cookies string
}

// Start returns the first login step, asking the user for the full cookie string
// extracted from an authenticated TikTok browser session.
//
// To obtain your cookie string:
//  1. Log in to TikTok in your browser (the in-app browser works too).
//  2. Open DevTools → Network, reload the page, and click any tiktok.com request.
//  3. Under Request Headers, find the "Cookie:" header and copy its full value.
func (tl *TikTokLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: "com.github.httpjamesm.matrix_tiktok.enter_cookies",
		Instructions: "Paste the full Cookie header string from an authenticated TikTok browser session.\n\n" +
			"How to get it:\n" +
			"1. Open TikTok in a browser (or in-app browser) and log in.\n" +
			"2. Open DevTools → Network tab, reload the page, and click any request to www.tiktok.com.\n" +
			"3. In the Request Headers panel, find the Cookie: header and copy its entire value.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypePassword,
					ID:          "cookies",
					Name:        "Cookie string",
					Description: "The full value of the Cookie: request header from an authenticated tiktok.com browser request",
				},
			},
		},
	}, nil
}

// SubmitUserInput is called each time the user provides values for the current
// input step. For TikTok we have a single step, so this always attempts to
// validate the session and finish the login.
func (tl *TikTokLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	cookies, ok := input["cookies"]
	if !ok || cookies == "" {
		return nil, fmt.Errorf("cookies is required")
	}

	tl.cookies = cookies

	return tl.finishLogin(ctx)
}

// finishLogin validates the session against the TikTok API, then persists the
// UserLogin and returns the completion step.
func (tl *TikTokLogin) finishLogin(ctx context.Context) (*bridgev2.LoginStep, error) {
	// TODO: replace this stub with a real call to your TikTok PoC client.
	// Example:
	//   client := tiktok.NewClient(tl.cookies)
	//   selfUser, err := client.GetSelf(ctx)
	//   if err != nil {
	//       return nil, fmt.Errorf("failed to validate session: %w", err)
	//   }

	// Stub self-user info — replace with real API response fields.
	selfUserID := "unknown" // e.g. selfUser.UniqueID or selfUser.ID
	selfUsername := "unknown"
	selfDisplayName := "Unknown TikTok User"

	// Construct the UserLogin record.
	ul, err := tl.User.NewLogin(ctx, &database.UserLogin{
		ID:         makeUserLoginID(selfUserID),
		RemoteName: selfDisplayName,
		Metadata: &UserLoginMetadata{
			UserID:   selfUserID,
			Username: selfUsername,
			Cookies:  tl.cookies,
		},
	}, &bridgev2.NewLoginParams{
		LoadUserLogin: func(ctx context.Context, login *bridgev2.UserLogin) error {
			meta := login.Metadata.(*UserLoginMetadata)
			login.Client = &TikTokClient{
				connector: tl.Connector,
				userLogin: login,
				meta:      meta,
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "com.github.httpjamesm.matrix_tiktok.complete",
		Instructions: fmt.Sprintf("Successfully logged in as %s", selfUsername),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

// Cancel is called if the user aborts the login flow before it completes.
// There are no persistent connections to tear down at login time for TikTok,
// so nothing needs to happen here.
func (tl *TikTokLogin) Cancel() {}
