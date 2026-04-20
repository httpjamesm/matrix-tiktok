package libtiktok

import (
	"encoding/json"
	"strings"
	"testing"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

func TestNormalizedUploadFileName(t *testing.T) {
	if got := normalizedUploadFileName("", "image/png"); got != "image.png" {
		t.Fatalf("normalizedUploadFileName() = %q", got)
	}
	if got := normalizedUploadFileName("photo", "image/jpeg"); got != "photo.jpg" {
		t.Fatalf("normalizedUploadFileName() = %q", got)
	}
}

func TestBuildMediaUploadConfigPayload(t *testing.T) {
	payload, err := buildMediaUploadConfigPayload("device-id", "ms-token", "verify-fp")
	if err != nil {
		t.Fatalf("buildMediaUploadConfigPayload: %v", err)
	}

	var req tiktokpb.MediaUploadConfigRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshalProto: %v", err)
	}
	if req.GetMessageType() != 2059 {
		t.Fatalf("message_type = %d", req.GetMessageType())
	}
	if req.GetSubCommand() != 10007 {
		t.Fatalf("sub_command = %d", req.GetSubCommand())
	}
	if req.GetPayload().GetImagex() != "" {
		t.Fatalf("payload.imagex = %q", req.GetPayload().GetImagex())
	}
	if req.GetDeviceId() != "device-id" {
		t.Fatalf("device_id = %q", req.GetDeviceId())
	}
}

func TestBuildMediaAuthorizationIncludesContentHash(t *testing.T) {
	rawQuery := buildSigV4CanonicalQuery([]orderedQueryParam{
		{Key: "Action", Value: "ApplyImageUpload"},
		{Key: "Version", Value: "2018-08-01"},
		{Key: "ServiceId", Value: "svc"},
	})
	_, payloadHash, auth, err := buildMediaAuthorization("GET", "imagex-upload-sg.tiktok.com", "/", rawQuery, nil, "application/x-www-form-urlencoded; charset=utf-8", imageXSigningProfile{
		Name:                 "content-type+content-sha256",
		IncludeHost:          true,
		IncludeContentType:   true,
		IncludeContentSHA256: true,
	}, &imageUploadConfig{
		AccessKeyID:   "AKTP-test",
		SecurityToken: "token",
		SecretKey:     "secret",
	})
	if err != nil {
		t.Fatalf("buildMediaAuthorization: %v", err)
	}
	if payloadHash == "" {
		t.Fatal("payloadHash should be set")
	}
	if !strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token") {
		t.Fatalf("auth header missing expected signed headers: %s", auth)
	}
	if !strings.Contains(auth, "/aws4_request, SignedHeaders=") {
		t.Fatalf("auth header missing expected credential scope: %s", auth)
	}
	if !strings.Contains(auth, "/imagex/aws4_request") {
		t.Fatalf("auth header should scope imagex service: %s", auth)
	}
}

func TestBuildMediaAuthorizationVODServiceScope(t *testing.T) {
	rawQuery := buildSigV4CanonicalQuery([]orderedQueryParam{
		{Key: "Action", Value: "ApplyUploadInner"},
		{Key: "Version", Value: "2020-11-19"},
		{Key: "SpaceName", Value: "tiktok-dm"},
		{Key: "FileType", Value: "video"},
		{Key: "IsInner", Value: "1"},
		{Key: "FileSize", Value: "123"},
		{Key: "s", Value: "abc"},
	})
	_, _, auth, err := buildMediaAuthorization("GET", "vod-upload-sg.tiktok.com", "/", rawQuery, nil, "application/x-www-form-urlencoded; charset=utf-8", imageXSigningProfile{
		Name: "date-token",
	}, &imageUploadConfig{
		AccessKeyID:    "AKTP-test",
		SecurityToken:  "token",
		SecretKey:      "secret",
		AWSServiceName: "vod",
	})
	if err != nil {
		t.Fatalf("buildMediaAuthorization: %v", err)
	}
	if !strings.Contains(auth, "/vod/aws4_request") {
		t.Fatalf("auth header should scope vod service: %s", auth)
	}
}

func TestBuildMediaAuthorizationDateTokenProfile(t *testing.T) {
	rawQuery := "Action=ApplyImageUpload&FileExtension=.png&FileSize=123&ServiceId=svc&Version=2018-08-01&s=abc"
	_, _, auth, err := buildMediaAuthorization("GET", "imagex-upload-sg.tiktok.com", "/", rawQuery, nil, "application/x-www-form-urlencoded; charset=utf-8", imageXSigningProfile{
		Name: "date-token",
	}, &imageUploadConfig{
		AccessKeyID:   "AKTP-test",
		SecurityToken: "token",
		SecretKey:     "secret",
	})
	if err != nil {
		t.Fatalf("buildMediaAuthorization: %v", err)
	}
	if !strings.Contains(auth, "SignedHeaders=x-amz-date;x-amz-security-token") {
		t.Fatalf("auth header missing expected date-token signed headers: %s", auth)
	}
}

func TestBuildOrderedQueryStringPreservesOrder(t *testing.T) {
	got := buildOrderedQueryString([]orderedQueryParam{
		{Key: "Action", Value: "ApplyImageUpload"},
		{Key: "Version", Value: "2018-08-01"},
		{Key: "ServiceId", Value: "svc"},
		{Key: "FileSize", Value: "123"},
	})
	want := "Action=ApplyImageUpload&Version=2018-08-01&ServiceId=svc&FileSize=123"
	if got != want {
		t.Fatalf("buildOrderedQueryString() = %q, want %q", got, want)
	}
}

func TestAppliedUploadFromApplyResponseInnerUploadAddress(t *testing.T) {
	const raw = `{
		"ResponseMetadata":{"RequestId":"req"},
		"Result":{
			"UploadAddress":null,
			"InnerUploadAddress":{"UploadNodes":[{
				"Vid":"v1",
				"StoreInfos":[{"StoreUri":"tos/x","Auth":"SpaceKey/...","UploadID":"u1"}],
				"UploadHost":"tos-my216-up.tiktokcdn.com",
				"SessionKey":"sess-token"
			}]}
		}
	}`
	var resp applyImageUploadResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := appliedUploadFromApplyResponse(&resp)
	if err != nil {
		t.Fatalf("appliedUploadFromApplyResponse: %v", err)
	}
	if got.StoreURI != "tos/x" || got.UploadHost != "tos-my216-up.tiktokcdn.com" || got.Auth == "" || got.SessionKey != "sess-token" {
		t.Fatalf("unexpected applied: %+v", got)
	}
}

func TestBuildSigV4CanonicalQuerySortsByParameterName(t *testing.T) {
	got := buildSigV4CanonicalQuery([]orderedQueryParam{
		{Key: "Version", Value: "2020-11-19"},
		{Key: "Action", Value: "ApplyUploadInner"},
		{Key: "SpaceName", Value: "tiktok-dm"},
		{Key: "FileType", Value: "video"},
		{Key: "IsInner", Value: "1"},
		{Key: "FileSize", Value: "1780826"},
		{Key: "s", Value: "xyz"},
	})
	want := "Action=ApplyUploadInner&FileSize=1780826&FileType=video&IsInner=1&SpaceName=tiktok-dm&Version=2020-11-19&s=xyz"
	if got != want {
		t.Fatalf("buildSigV4CanonicalQuery() = %q, want %q", got, want)
	}
}
