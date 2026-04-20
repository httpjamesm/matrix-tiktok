package libtiktok

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"mime"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

const (
	mediaUploadConfigPath = "/v1/media/upload_config"
	mediaUploadRegion     = "cn-north-1"
	vodUploadSpaceName    = "tiktok-dm"
	vodAPIVersion         = "2020-11-19"
)

type OutgoingImage struct {
	Data     []byte
	FileName string
	MimeType string
}

// OutgoingVideo is a raw video blob uploaded through TikTok VOD (vod-upload host).
type OutgoingVideo struct {
	Data     []byte
	FileName string
	MimeType string
}

type imageUploadConfig struct {
	ServiceID     string
	Host          string
	AccessKeyID   string
	SecurityToken string
	SecretKey     string
	// AWSServiceName is the SigV4 credential scope service ("imagex" or "vod").
	// Empty defaults to "imagex".
	AWSServiceName string
}

func (cfg *imageUploadConfig) awsServiceForSigning() string {
	if strings.TrimSpace(cfg.AWSServiceName) != "" {
		return cfg.AWSServiceName
	}
	return "imagex"
}

type appliedImageUpload struct {
	StoreURI   string
	UploadHost string
	Auth       string
	SessionKey string
}

type uploadedPrivateImage struct {
	URI        string
	DecryptKey string
	Width      int
	Height     int
	Size       int
	FileName   string
}

// uploadedPrivateVideo holds VOD identifiers and dimensions after CommitUploadInner.
type uploadedPrivateVideo struct {
	Vid        string
	PosterURI  string
	Width      int
	Height     int
	DurationMs int
	Size       int
	FileName   string
	Codec      string
}

type orderedQueryParam struct {
	Key   string
	Value string
}

type imageXSigningProfile struct {
	Name                 string
	IncludeHost          bool
	SendContentType      bool
	IncludeContentType   bool
	IncludeContentSHA256 bool
}

// applyStoreInfo is a single TOS slice/store row from Apply* upload responses.
type applyStoreInfo struct {
	StoreURI string `json:"StoreUri"`
	Auth     string `json:"Auth"`
	UploadID string `json:"UploadID"`
}

// applyImageUploadResponse models ApplyImageUpload and ApplyUploadInner JSON.
// ImageX fills Result.UploadAddress; VOD inner upload uses Result.InnerUploadAddress
// with UploadAddress null.
type applyImageUploadResponse struct {
	ResponseMetadata struct {
		RequestID string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		UploadAddress *struct {
			StoreInfos  []applyStoreInfo `json:"StoreInfos"`
			UploadHosts []string         `json:"UploadHosts"`
			SessionKey  string           `json:"SessionKey"`
		} `json:"UploadAddress"`
		InnerUploadAddress *struct {
			UploadNodes []applyInnerUploadNode `json:"UploadNodes"`
		} `json:"InnerUploadAddress"`
	} `json:"Result"`
}

type applyInnerUploadNode struct {
	Vid        string           `json:"Vid"`
	StoreInfos []applyStoreInfo `json:"StoreInfos"`
	UploadHost string           `json:"UploadHost"`
	SessionKey string           `json:"SessionKey"`
}

func appliedUploadFromApplyResponse(result *applyImageUploadResponse) (*appliedImageUpload, error) {
	if ua := result.Result.UploadAddress; ua != nil {
		if len(ua.StoreInfos) > 0 && len(ua.UploadHosts) > 0 && ua.SessionKey != "" {
			si := ua.StoreInfos[0]
			if si.StoreURI != "" && si.Auth != "" {
				return &appliedImageUpload{
					StoreURI:   si.StoreURI,
					UploadHost: ua.UploadHosts[0],
					Auth:       si.Auth,
					SessionKey: ua.SessionKey,
				}, nil
			}
		}
	}
	if inner := result.Result.InnerUploadAddress; inner != nil {
		for _, node := range inner.UploadNodes {
			if len(node.StoreInfos) == 0 || node.UploadHost == "" || node.SessionKey == "" {
				continue
			}
			si := node.StoreInfos[0]
			if si.StoreURI == "" || si.Auth == "" {
				continue
			}
			return &appliedImageUpload{
				StoreURI:   si.StoreURI,
				UploadHost: node.UploadHost,
				Auth:       si.Auth,
				SessionKey: node.SessionKey,
			}, nil
		}
	}
	return nil, fmt.Errorf("response missing upload address")
}

type uploadImageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type commitImageUploadRequest struct {
	SessionKey string `json:"SessionKey"`
	Functions  []struct {
		Name  string `json:"name"`
		Input struct {
			Config struct {
				Copies string `json:"copies"`
			} `json:"Config"`
		} `json:"input"`
	} `json:"Functions"`
}

type commitVideoUploadRequest struct {
	SessionKey string                      `json:"SessionKey"`
	Functions  []commitVideoUploadFunction `json:"Functions"`
}

type commitVideoUploadFunction struct {
	Name  string                   `json:"name"`
	Input commitVideoSnapshotInput `json:"input"`
}

type commitVideoSnapshotInput struct {
	SnapshotTime    int  `json:"SnapshotTime"`
	SkipBlackDetect bool `json:"SkipBlackDetect"`
}

type commitVideoUploadResponse struct {
	ResponseMetadata struct {
		RequestID string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		Results []struct {
			Vid       string `json:"Vid"`
			PosterUri string `json:"PosterUri"`
			VideoMeta struct {
				Uri      string  `json:"Uri"`
				Height   int     `json:"Height"`
				Width    int     `json:"Width"`
				Duration float64 `json:"Duration"`
				Size     int     `json:"Size"`
				Format   string  `json:"Format"`
				Codec    string  `json:"Codec"`
				FileType string  `json:"FileType"`
			} `json:"VideoMeta"`
		} `json:"Results"`
	} `json:"Result"`
}

type commitImageUploadResponse struct {
	ResponseMetadata struct {
		RequestID string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		Results []struct {
			Encryption struct {
				URI       string            `json:"Uri"`
				SecretKey string            `json:"SecretKey"`
				Extra     map[string]string `json:"Extra"`
			} `json:"Encryption"`
		} `json:"Results"`
		PluginResult []struct {
			ImageURI    string `json:"ImageUri"`
			ImageWidth  int    `json:"ImageWidth"`
			ImageHeight int    `json:"ImageHeight"`
			ImageSize   int    `json:"ImageSize"`
		} `json:"PluginResult"`
	} `json:"Result"`
}

type imageUploadAPIError struct {
	Operation string
	Profile   string
	Code      string
	RequestID string
	Message   string
}

func (e *imageUploadAPIError) Error() string {
	if e.Profile != "" {
		return fmt.Sprintf("%s [%s] error %s (%s): %s", e.Operation, e.Profile, e.Code, e.RequestID, e.Message)
	}
	return fmt.Sprintf("%s error %s (%s): %s", e.Operation, e.Code, e.RequestID, e.Message)
}

func (c *Client) uploadImage(ctx context.Context, image *OutgoingImage) (*uploadedPrivateImage, error) {
	if image == nil {
		return nil, fmt.Errorf("nil image payload")
	}
	if len(image.Data) == 0 {
		return nil, fmt.Errorf("image payload is empty")
	}

	cfg, err := c.getImageUploadConfig(ctx)
	if err != nil {
		return nil, err
	}
	applied, err := c.applyImageUpload(ctx, cfg, image)
	if err != nil {
		return nil, err
	}
	if err := c.uploadImageBytes(ctx, applied, image.Data); err != nil {
		return nil, err
	}
	committed, err := c.commitImageUpload(ctx, cfg, applied.SessionKey)
	if err != nil {
		return nil, err
	}
	committed.FileName = normalizedUploadFileName(image.FileName, image.MimeType)
	return committed, nil
}

// uploadVideo uploads a video through TikTok DM VOD (ApplyUploadInner / binary upload / CommitUploadInner).
func (c *Client) uploadVideo(ctx context.Context, video *OutgoingVideo) (*uploadedPrivateVideo, error) {
	if video == nil {
		return nil, fmt.Errorf("nil video payload")
	}
	if len(video.Data) == 0 {
		return nil, fmt.Errorf("video payload is empty")
	}

	cfg, err := c.getVODUploadConfig(ctx)
	if err != nil {
		return nil, err
	}
	applied, err := c.applyVideoUpload(ctx, cfg, video)
	if err != nil {
		return nil, err
	}
	if err := c.uploadImageBytes(ctx, applied, video.Data); err != nil {
		return nil, err
	}
	committed, err := c.commitVideoUpload(ctx, cfg, applied.SessionKey)
	if err != nil {
		return nil, err
	}
	committed.FileName = normalizedUploadFileName(video.FileName, video.MimeType)
	return committed, nil
}

func buildMediaUploadConfigPayload(deviceID, msToken, verifyFP string) ([]byte, error) {
	msg := &tiktokpb.MediaUploadConfigRequest{
		MessageType:    protoUint64(2059),
		SubCommand:     protoUint64(10007),
		ClientVersion:  protoString("1.6.0"),
		Options:        emptyProtoMessage(),
		PlatformFlag:   protoUint64(3),
		Reserved_6:     protoUint64(0),
		GitHash:        protoString(""),
		DeviceId:       protoString(deviceID),
		ClientPlatform: protoString("web"),
		Metadata:       metadataKVsToProto(buildMetadata(deviceID, msToken, verifyFP)),
		FinalFlag:      protoUint64(1),
		Payload: &tiktokpb.MediaUploadConfigRequestPayload{
			Imagex: protoString(""),
		},
	}
	return marshalProto(msg)
}

func (c *Client) fetchMediaUploadConfigEntries(ctx context.Context) ([]*tiktokpb.MediaUploadConfigEntry, error) {
	cookie := c.rIA.Header.Get("Cookie")

	universalData, err := c.getMessagesUniversalData()
	if err != nil {
		return nil, fmt.Errorf("get universal data: %w", err)
	}
	appContext, err := universalData.getAppContext()
	if err != nil {
		return nil, fmt.Errorf("get appContext: %w", err)
	}
	deviceID, ok := appContext["wid"].(string)
	if !ok {
		return nil, fmt.Errorf("wid not found in appContext")
	}

	msToken := extractCookie(cookie, "msToken")
	verifyFP := extractCookie(cookie, "s_v_web_id")
	payload, err := buildMediaUploadConfigPayload(deviceID, msToken, verifyFP)
	if err != nil {
		return nil, fmt.Errorf("build media upload config payload: %w", err)
	}

	resp, err := c.rIA.R().
		SetContext(ctx).
		SetHeader("Accept", "application/x-protobuf").
		SetHeader("Content-Type", "application/x-protobuf").
		SetHeader("Cache-Control", "no-cache").
		SetHeader("Origin", "https://www.tiktok.com").
		SetQueryParams(map[string]string{
			"aid":             imAID,
			"version_code":    "1.0.0",
			"app_name":        "tiktok_web",
			"device_platform": "web_pc",
			"msToken":         msToken,
			"X-Bogus":         randomBogus(),
		}).
		SetBody(payload).
		Post(mediaUploadConfigPath)
	if err != nil {
		return nil, fmt.Errorf("POST media upload config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("media upload config returned %d: %s", resp.StatusCode(), resp.String())
	}

	var cfgResp tiktokpb.MediaUploadConfigResponse
	if err := unmarshalProto(resp.Body(), &cfgResp); err != nil {
		return nil, fmt.Errorf("decode media upload config: %w", err)
	}

	return cfgResp.GetPayload().GetImagex().GetEntries(), nil
}

func (c *Client) getImageUploadConfig(ctx context.Context) (*imageUploadConfig, error) {
	entries, err := c.fetchMediaUploadConfigEntries(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.GetServiceId() == "" || entry.GetHost() == "" || entry.GetAccessKeyId() == "" || entry.GetSecurityToken() == "" || entry.GetSecretAccessKey() == "" {
			continue
		}
		if entry.GetUploadKind() != 1 && !strings.Contains(entry.GetHost(), "imagex-upload") {
			continue
		}
		return &imageUploadConfig{
			ServiceID:     entry.GetServiceId(),
			Host:          entry.GetHost(),
			AccessKeyID:   entry.GetAccessKeyId(),
			SecurityToken: entry.GetSecurityToken(),
			SecretKey:     entry.GetSecretAccessKey(),
		}, nil
	}

	return nil, fmt.Errorf("no image upload config found in response")
}

func (c *Client) getVODUploadConfig(ctx context.Context) (*imageUploadConfig, error) {
	entries, err := c.fetchMediaUploadConfigEntries(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.GetHost() == "" || entry.GetAccessKeyId() == "" || entry.GetSecurityToken() == "" || entry.GetSecretAccessKey() == "" {
			continue
		}
		host := strings.ToLower(entry.GetHost())
		if entry.GetUploadKind() != 2 && !strings.Contains(host, "vod-upload") {
			continue
		}
		return &imageUploadConfig{
			ServiceID:      entry.GetServiceId(),
			Host:           entry.GetHost(),
			AccessKeyID:    entry.GetAccessKeyId(),
			SecurityToken:  entry.GetSecurityToken(),
			SecretKey:      entry.GetSecretAccessKey(),
			AWSServiceName: "vod",
		}, nil
	}

	return nil, fmt.Errorf("no VOD upload config found in response")
}

func (c *Client) applyImageUpload(ctx context.Context, cfg *imageUploadConfig, image *OutgoingImage) (*appliedImageUpload, error) {
	return c.applyImageUploadWithAccessKey(ctx, cfg, image, cfg.AccessKeyID)
}

func (c *Client) applyImageUploadWithAccessKey(ctx context.Context, cfg *imageUploadConfig, image *OutgoingImage, accessKeyID string) (*appliedImageUpload, error) {
	localCfg := *cfg
	localCfg.AccessKeyID = accessKeyID
	query := []orderedQueryParam{
		{Key: "Action", Value: "ApplyImageUpload"},
		{Key: "FileExtension", Value: normalizedUploadExtension(image.FileName, image.MimeType)},
		{Key: "FileSize", Value: strconv.Itoa(len(image.Data))},
		{Key: "ServiceId", Value: cfg.ServiceID},
		{Key: "Version", Value: "2018-08-01"},
		{Key: "s", Value: randomUploadIdentifier(11)},
	}
	rawQuery := buildSigV4CanonicalQuery(query)
	var attemptErrors []string
	for _, profile := range imageXSigningProfiles("GET") {
		applied, err := c.applyImageUploadWithProfile(ctx, &localCfg, rawQuery, profile)
		if err == nil {
			return applied, nil
		}
		attemptErrors = append(attemptErrors, err.Error())
		if apiErr, ok := err.(*imageUploadAPIError); ok && isRetryableSignatureProfileError(apiErr.Code) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("apply image upload failed for all signing profiles: %s", strings.Join(attemptErrors, "; "))
}

func (c *Client) applyImageUploadWithProfile(ctx context.Context, cfg *imageUploadConfig, rawQuery string, profile imageXSigningProfile) (*appliedImageUpload, error) {
	contentType := "application/x-www-form-urlencoded; charset=utf-8"
	amzDate, payloadHash, auth, err := buildMediaAuthorization("GET", cfg.Host, "/", rawQuery, nil, contentType, profile, cfg)
	if err != nil {
		return nil, err
	}

	client := newMediaUploadClient()
	var result applyImageUploadResponse
	req := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("x-amz-date", amzDate).
		SetHeader("x-amz-security-token", cfg.SecurityToken).
		SetResult(&result)
	if profile.SendContentType || profile.IncludeContentType {
		req.SetHeader("Content-Type", contentType)
	}
	if profile.IncludeContentSHA256 {
		req.SetHeader("x-amz-content-sha256", payloadHash)
	}
	resp, err := req.Get("https://" + cfg.Host + "/?" + rawQuery)
	if err != nil {
		return nil, fmt.Errorf("apply image upload [%s]: %w", profile.Name, err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("apply image upload [%s] returned %d: %s", profile.Name, resp.StatusCode(), resp.String())
	}
	if result.ResponseMetadata.Error != nil {
		return nil, &imageUploadAPIError{
			Operation: "apply image upload",
			Profile:   profile.Name,
			Code:      result.ResponseMetadata.Error.Code,
			RequestID: result.ResponseMetadata.RequestID,
			Message:   result.ResponseMetadata.Error.Message,
		}
	}

	applied, err := appliedUploadFromApplyResponse(&result)
	if err != nil {
		return nil, fmt.Errorf("apply image upload [%s] %w: %s", profile.Name, err, strings.TrimSpace(resp.String()))
	}
	return applied, nil
}

func (c *Client) uploadImageBytes(ctx context.Context, applied *appliedImageUpload, data []byte) error {
	client := newMediaUploadClient()
	var result uploadImageResponse
	crc32Hex := fmt.Sprintf("%08x", crc32.ChecksumIEEE(data))
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", applied.Auth).
		SetHeader("Content-CRC32", crc32Hex).
		SetHeader("Content-Type", "application/octet-stream").
		SetHeader("Content-Disposition", `attachment; filename="undefined"`).
		SetBody(data).
		SetResult(&result).
		Post("https://" + applied.UploadHost + "/upload/v1/" + applied.StoreURI)
	if err != nil {
		return fmt.Errorf("upload image bytes: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("upload image bytes returned %d: %s", resp.StatusCode(), resp.String())
	}
	if result.Code != 2000 {
		return fmt.Errorf("upload image bytes returned code %d: %s", result.Code, result.Message)
	}
	return nil
}

func (c *Client) commitImageUpload(ctx context.Context, cfg *imageUploadConfig, sessionKey string) (*uploadedPrivateImage, error) {
	return c.commitImageUploadWithAccessKey(ctx, cfg, sessionKey, cfg.AccessKeyID)
}

func (c *Client) commitImageUploadWithAccessKey(ctx context.Context, cfg *imageUploadConfig, sessionKey, accessKeyID string) (*uploadedPrivateImage, error) {
	localCfg := *cfg
	localCfg.AccessKeyID = accessKeyID
	reqBody := commitImageUploadRequest{SessionKey: sessionKey}
	reqBody.Functions = make([]struct {
		Name  string `json:"name"`
		Input struct {
			Config struct {
				Copies string `json:"copies"`
			} `json:"Config"`
		} `json:"input"`
	}, 1)
	reqBody.Functions[0].Name = "Encryption"
	reqBody.Functions[0].Input.Config.Copies = "cipher_v2"

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal commit image upload body: %w", err)
	}
	query := []orderedQueryParam{
		{Key: "Action", Value: "CommitImageUpload"},
		{Key: "ServiceId", Value: cfg.ServiceID},
		{Key: "Version", Value: "2018-08-01"},
	}
	rawQuery := buildSigV4CanonicalQuery(query)
	var attemptErrors []string
	for _, profile := range imageXSigningProfiles("POST") {
		committed, err := c.commitImageUploadWithProfile(ctx, &localCfg, rawQuery, body, profile)
		if err == nil {
			return committed, nil
		}
		attemptErrors = append(attemptErrors, err.Error())
		if apiErr, ok := err.(*imageUploadAPIError); ok && isRetryableSignatureProfileError(apiErr.Code) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("commit image upload failed for all signing profiles: %s", strings.Join(attemptErrors, "; "))
}

func (c *Client) commitImageUploadWithProfile(ctx context.Context, cfg *imageUploadConfig, rawQuery string, body []byte, profile imageXSigningProfile) (*uploadedPrivateImage, error) {
	contentType := "application/json"
	amzDate, payloadHash, auth, err := buildMediaAuthorization("POST", cfg.Host, "/", rawQuery, body, contentType, profile, cfg)
	if err != nil {
		return nil, err
	}

	client := newMediaUploadClient()
	var result commitImageUploadResponse
	req := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("x-amz-date", amzDate).
		SetHeader("x-amz-security-token", cfg.SecurityToken).
		SetBody(body).
		SetResult(&result)
	if profile.SendContentType || profile.IncludeContentType {
		req.SetHeader("Content-Type", contentType)
	}
	if profile.IncludeContentSHA256 {
		req.SetHeader("x-amz-content-sha256", payloadHash)
	}
	resp, err := req.Post("https://" + cfg.Host + "/?" + rawQuery)
	if err != nil {
		return nil, fmt.Errorf("commit image upload [%s]: %w", profile.Name, err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("commit image upload [%s] returned %d: %s", profile.Name, resp.StatusCode(), resp.String())
	}
	if result.ResponseMetadata.Error != nil {
		return nil, &imageUploadAPIError{
			Operation: "commit image upload",
			Profile:   profile.Name,
			Code:      result.ResponseMetadata.Error.Code,
			RequestID: result.ResponseMetadata.RequestID,
			Message:   result.ResponseMetadata.Error.Message,
		}
	}
	if len(result.Result.Results) == 0 {
		return nil, fmt.Errorf("commit image upload [%s] response missing results: %s", profile.Name, strings.TrimSpace(resp.String()))
	}

	enc := result.Result.Results[0].Encryption
	if enc.URI == "" || enc.SecretKey == "" {
		return nil, fmt.Errorf("commit image upload response missing encryption data")
	}
	out := &uploadedPrivateImage{
		URI:        enc.URI,
		DecryptKey: enc.SecretKey,
		Width:      parseStringInt(enc.Extra["img_width"]),
		Height:     parseStringInt(enc.Extra["img_height"]),
		Size:       parseStringInt(enc.Extra["img_size"]),
	}
	if len(result.Result.PluginResult) > 0 {
		plugin := result.Result.PluginResult[0]
		if plugin.ImageURI != "" {
			out.URI = plugin.ImageURI
		}
		if plugin.ImageWidth > 0 {
			out.Width = plugin.ImageWidth
		}
		if plugin.ImageHeight > 0 {
			out.Height = plugin.ImageHeight
		}
		if plugin.ImageSize > 0 {
			out.Size = plugin.ImageSize
		}
	}
	return out, nil
}

func (c *Client) applyVideoUpload(ctx context.Context, cfg *imageUploadConfig, video *OutgoingVideo) (*appliedImageUpload, error) {
	return c.applyVideoUploadWithAccessKey(ctx, cfg, video, cfg.AccessKeyID)
}

func (c *Client) applyVideoUploadWithAccessKey(ctx context.Context, cfg *imageUploadConfig, video *OutgoingVideo, accessKeyID string) (*appliedImageUpload, error) {
	localCfg := *cfg
	localCfg.AccessKeyID = accessKeyID
	query := []orderedQueryParam{
		{Key: "Action", Value: "ApplyUploadInner"},
		{Key: "Version", Value: vodAPIVersion},
		{Key: "SpaceName", Value: vodUploadSpaceName},
		{Key: "FileType", Value: "video"},
		{Key: "IsInner", Value: "1"},
		{Key: "FileSize", Value: strconv.Itoa(len(video.Data))},
		{Key: "s", Value: randomUploadIdentifier(11)},
	}
	rawQuery := buildSigV4CanonicalQuery(query)
	var attemptErrors []string
	for _, profile := range imageXSigningProfiles("GET") {
		applied, err := c.applyVideoUploadWithProfile(ctx, &localCfg, rawQuery, profile)
		if err == nil {
			return applied, nil
		}
		attemptErrors = append(attemptErrors, err.Error())
		if apiErr, ok := err.(*imageUploadAPIError); ok && isRetryableSignatureProfileError(apiErr.Code) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("apply video upload failed for all signing profiles: %s", strings.Join(attemptErrors, "; "))
}

func (c *Client) applyVideoUploadWithProfile(ctx context.Context, cfg *imageUploadConfig, rawQuery string, profile imageXSigningProfile) (*appliedImageUpload, error) {
	contentType := "application/x-www-form-urlencoded; charset=utf-8"
	amzDate, payloadHash, auth, err := buildMediaAuthorization("GET", cfg.Host, "/", rawQuery, nil, contentType, profile, cfg)
	if err != nil {
		return nil, err
	}

	client := newMediaUploadClient()
	var result applyImageUploadResponse
	req := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("x-amz-date", amzDate).
		SetHeader("x-amz-security-token", cfg.SecurityToken).
		SetResult(&result)
	if profile.SendContentType || profile.IncludeContentType {
		req.SetHeader("Content-Type", contentType)
	}
	if profile.IncludeContentSHA256 {
		req.SetHeader("x-amz-content-sha256", payloadHash)
	}
	resp, err := req.Get("https://" + cfg.Host + "/?" + rawQuery)
	if err != nil {
		return nil, fmt.Errorf("apply video upload [%s]: %w", profile.Name, err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("apply video upload [%s] returned %d: %s", profile.Name, resp.StatusCode(), resp.String())
	}
	if result.ResponseMetadata.Error != nil {
		return nil, &imageUploadAPIError{
			Operation: "apply video upload",
			Profile:   profile.Name,
			Code:      result.ResponseMetadata.Error.Code,
			RequestID: result.ResponseMetadata.RequestID,
			Message:   result.ResponseMetadata.Error.Message,
		}
	}

	applied, err := appliedUploadFromApplyResponse(&result)
	if err != nil {
		return nil, fmt.Errorf("apply video upload [%s] %w: %s", profile.Name, err, strings.TrimSpace(resp.String()))
	}
	return applied, nil
}

func (c *Client) commitVideoUpload(ctx context.Context, cfg *imageUploadConfig, sessionKey string) (*uploadedPrivateVideo, error) {
	return c.commitVideoUploadWithAccessKey(ctx, cfg, sessionKey, cfg.AccessKeyID)
}

func (c *Client) commitVideoUploadWithAccessKey(ctx context.Context, cfg *imageUploadConfig, sessionKey, accessKeyID string) (*uploadedPrivateVideo, error) {
	localCfg := *cfg
	localCfg.AccessKeyID = accessKeyID
	reqBody := commitVideoUploadRequest{SessionKey: sessionKey}
	reqBody.Functions = []commitVideoUploadFunction{{
		Name: "Snapshot",
		Input: commitVideoSnapshotInput{
			SnapshotTime:    0,
			SkipBlackDetect: true,
		},
	}}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal commit video upload body: %w", err)
	}
	query := []orderedQueryParam{
		{Key: "Action", Value: "CommitUploadInner"},
		{Key: "Version", Value: vodAPIVersion},
		{Key: "SpaceName", Value: vodUploadSpaceName},
	}
	rawQuery := buildSigV4CanonicalQuery(query)
	var attemptErrors []string
	for _, profile := range imageXSigningProfiles("POST") {
		committed, err := c.commitVideoUploadWithProfile(ctx, &localCfg, rawQuery, body, profile)
		if err == nil {
			return committed, nil
		}
		attemptErrors = append(attemptErrors, err.Error())
		if apiErr, ok := err.(*imageUploadAPIError); ok && isRetryableSignatureProfileError(apiErr.Code) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("commit video upload failed for all signing profiles: %s", strings.Join(attemptErrors, "; "))
}

func (c *Client) commitVideoUploadWithProfile(ctx context.Context, cfg *imageUploadConfig, rawQuery string, body []byte, profile imageXSigningProfile) (*uploadedPrivateVideo, error) {
	contentType := "application/json"
	amzDate, payloadHash, auth, err := buildMediaAuthorization("POST", cfg.Host, "/", rawQuery, body, contentType, profile, cfg)
	if err != nil {
		return nil, err
	}

	client := newMediaUploadClient()
	var result commitVideoUploadResponse
	req := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", auth).
		SetHeader("x-amz-date", amzDate).
		SetHeader("x-amz-security-token", cfg.SecurityToken).
		SetBody(body).
		SetResult(&result)
	if profile.SendContentType || profile.IncludeContentType {
		req.SetHeader("Content-Type", contentType)
	}
	if profile.IncludeContentSHA256 {
		req.SetHeader("x-amz-content-sha256", payloadHash)
	}
	resp, err := req.Post("https://" + cfg.Host + "/?" + rawQuery)
	if err != nil {
		return nil, fmt.Errorf("commit video upload [%s]: %w", profile.Name, err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("commit video upload [%s] returned %d: %s", profile.Name, resp.StatusCode(), resp.String())
	}
	if result.ResponseMetadata.Error != nil {
		return nil, &imageUploadAPIError{
			Operation: "commit video upload",
			Profile:   profile.Name,
			Code:      result.ResponseMetadata.Error.Code,
			RequestID: result.ResponseMetadata.RequestID,
			Message:   result.ResponseMetadata.Error.Message,
		}
	}
	if len(result.Result.Results) == 0 {
		return nil, fmt.Errorf("commit video upload [%s] response missing results: %s", profile.Name, strings.TrimSpace(resp.String()))
	}
	r0 := result.Result.Results[0]
	if r0.Vid == "" {
		return nil, fmt.Errorf("commit video upload response missing Vid")
	}
	meta := r0.VideoMeta
	durationMs := int(meta.Duration*1000.0 + 0.5)
	return &uploadedPrivateVideo{
		Vid:        r0.Vid,
		PosterURI:  r0.PosterUri,
		Width:      meta.Width,
		Height:     meta.Height,
		DurationMs: durationMs,
		Size:       meta.Size,
		Codec:      meta.Codec,
	}, nil
}

func isRetryableSignatureProfileError(code string) bool {
	switch strings.TrimSpace(code) {
	case "SignatureDoesNotMatch", "InvalidAuthorization":
		return true
	default:
		return false
	}
}

func imageXSigningProfiles(method string) []imageXSigningProfile {
	if method == "GET" {
		return []imageXSigningProfile{
			{Name: "date-token"},
		}
	}
	return []imageXSigningProfile{
		{Name: "date-token+content-sha256", SendContentType: true, IncludeContentSHA256: true},
	}
}

func newMediaUploadClient() *resty.Client {
	c := resty.New()
	c.SetHeader("User-Agent", DefaultUserAgent)
	c.SetHeader("Accept-Language", "en-US,en;q=0.9")
	return c
}

func buildMediaAuthorization(method, host, canonicalPath, rawQuery string, body []byte, contentType string, profile imageXSigningProfile, cfg *imageUploadConfig) (string, string, string, error) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateScope := now.Format("20060102")
	payloadHash := sha256Hex(body)
	contentType = strings.TrimSpace(contentType)
	headerLines := []string{
		"x-amz-date:" + amzDate,
		"x-amz-security-token:" + strings.TrimSpace(cfg.SecurityToken),
	}
	signedHeaders := []string{"x-amz-date", "x-amz-security-token"}
	if profile.IncludeHost {
		headerLines = append(headerLines, "host:"+strings.ToLower(host))
		signedHeaders = append(signedHeaders, "host")
	}
	if profile.IncludeContentType {
		headerLines = append(headerLines, "content-type:"+contentType)
		signedHeaders = append(signedHeaders, "content-type")
	}
	if profile.IncludeContentSHA256 {
		headerLines = append(headerLines, "x-amz-content-sha256:"+payloadHash)
		signedHeaders = append(signedHeaders, "x-amz-content-sha256")
	}
	sort.Strings(headerLines)
	sort.Strings(signedHeaders)
	canonicalHeaders := strings.Join(headerLines, "\n") + "\n"
	signedHeadersValue := strings.Join(signedHeaders, ";")
	canonicalRequest := strings.Join([]string{
		method,
		canonicalPath,
		rawQuery,
		canonicalHeaders,
		signedHeadersValue,
		payloadHash,
	}, "\n")

	scope := dateScope + "/" + mediaUploadRegion + "/" + cfg.awsServiceForSigning() + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+cfg.SecretKey), dateScope)
	signingKey = hmacSHA256(signingKey, mediaUploadRegion)
	signingKey = hmacSHA256(signingKey, cfg.awsServiceForSigning())
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	authorization := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.AccessKeyID, scope, signedHeadersValue, signature,
	)
	return amzDate, payloadHash, authorization, nil
}

func buildOrderedQueryString(query []orderedQueryParam) string {
	parts := make([]string, 0, len(query))
	for _, param := range query {
		parts = append(parts, param.Key+"="+awsPercentEncode(param.Value))
	}
	return strings.Join(parts, "&")
}

// buildSigV4CanonicalQuery returns the query string for the canonical request
// (AWS Signature V4: sort parameter names by UTF-8 code unit ascending).
func buildSigV4CanonicalQuery(query []orderedQueryParam) string {
	if len(query) <= 1 {
		return buildOrderedQueryString(query)
	}
	sorted := append([]orderedQueryParam(nil), query...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Key < sorted[j].Key
	})
	return buildOrderedQueryString(sorted)
}

func awsPercentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		case c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

func normalizedUploadFileName(fileName, mimeType string) string {
	fileName = strings.TrimSpace(filepath.Base(fileName))
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		if strings.HasPrefix(mimeType, "video/") {
			return "video" + normalizedUploadExtension("", mimeType)
		}
		return "image" + normalizedUploadExtension("", mimeType)
	}
	if filepath.Ext(fileName) == "" {
		return fileName + normalizedUploadExtension("", mimeType)
	}
	return fileName
}

func normalizedUploadExtension(fileName, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext != "" {
		return ext
	}
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	}
	if mimeType != "" {
		if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
			return strings.ToLower(exts[0])
		}
	}
	return ".bin"
}

func parseStringInt(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func randomUploadIdentifier(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, length)
	rnd := make([]byte, length)
	if _, err := rand.Read(rnd); err != nil {
		for i := range buf {
			buf[i] = alphabet[i%len(alphabet)]
		}
		return string(buf)
	}
	for i := range buf {
		buf[i] = alphabet[int(rnd[i])%len(alphabet)]
	}
	return string(buf)
}
