package dataapi

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

type InspectionPhotoInput struct {
	InspectionID   string
	Slot           int
	Version        int64
	SourceEventKey string
	IdempotencyKey string
	ContentType    string
	Image          []byte
}

type IssueStageInput struct {
	ScopeType      string
	ScopeID        string
	SourceEventKey string
	IdempotencyKey string
	ContentType    string
	Image          []byte
}

// StageIssueAsset uploads one image without counting it as an inspection slot.
func (c *Client) StageIssueAsset(ctx context.Context, actorMaxID string, input IssueStageInput) (StagedAsset, error) {
	if !validMaxID(actorMaxID) || !validUUID(input.ScopeID) || (input.ScopeType != "vehicle" && input.ScopeType != "trip" && input.ScopeType != "inspection") || !validKey(input.IdempotencyKey) || input.SourceEventKey == "" || len(input.SourceEventKey) > 200 || strings.ContainsAny(input.SourceEventKey, "\r\n") || len(input.Image) == 0 || len(input.Image) > 10<<20 || (input.ContentType != "image/jpeg" && input.ContentType != "image/png" && input.ContentType != "image/webp") {
		return StagedAsset{}, errors.New("data-api: invalid issue photo input")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="image"; filename="photo"`)
	header.Set("Content-Type", input.ContentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return StagedAsset{}, err
	}
	if _, err := part.Write(input.Image); err != nil {
		return StagedAsset{}, err
	}
	for name, value := range map[string]string{"purpose": "issue", "scope_type": input.ScopeType, "scope_id": input.ScopeID, "source_event_key": input.SourceEventKey} {
		if err := writer.WriteField(name, value); err != nil {
			return StagedAsset{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return StagedAsset{}, err
	}
	result, err := request[StagedAsset](ctx, c, http.MethodPost, "/assets/stage", actorMaxID, buffer.Bytes(), writer.FormDataContentType(), input.IdempotencyKey, nil, c.uploadClient)
	if err != nil {
		return StagedAsset{}, err
	}
	if !validUUID(result.AssetID) || result.ExpiresAt.IsZero() {
		return StagedAsset{}, errors.New("data-api: invalid staged asset result")
	}
	return result, nil
}

// UploadInspectionPhoto sends exactly one bounded image to a selected slot.
// The same multipart body and idempotency key are reused on retry.
func (c *Client) UploadInspectionPhoto(ctx context.Context, actorMaxID string, input InspectionPhotoInput) (PhotoUploadResult, error) {
	if !validMaxID(actorMaxID) || !validUUID(input.InspectionID) || input.Slot < 1 || input.Slot > 8 || input.Version < 1 || !validKey(input.IdempotencyKey) || input.SourceEventKey == "" || len(input.SourceEventKey) > 200 || strings.ContainsAny(input.SourceEventKey, "\r\n") || len(input.Image) == 0 || len(input.Image) > 10<<20 || (input.ContentType != "image/jpeg" && input.ContentType != "image/png" && input.ContentType != "image/webp") {
		return PhotoUploadResult{}, errors.New("data-api: invalid inspection photo input")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="image"; filename="photo"`)
	header.Set("Content-Type", input.ContentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return PhotoUploadResult{}, err
	}
	if _, err := part.Write(input.Image); err != nil {
		return PhotoUploadResult{}, err
	}
	if err := writer.WriteField("expected_version", strconv.FormatInt(input.Version, 10)); err != nil {
		return PhotoUploadResult{}, err
	}
	if err := writer.WriteField("source_event_key", input.SourceEventKey); err != nil {
		return PhotoUploadResult{}, err
	}
	if err := writer.Close(); err != nil {
		return PhotoUploadResult{}, err
	}
	path := "/inspections/" + input.InspectionID + "/photos/" + strconv.Itoa(input.Slot)
	result, err := request[PhotoUploadResult](ctx, c, http.MethodPost, path, actorMaxID, buffer.Bytes(), writer.FormDataContentType(), input.IdempotencyKey, nil, c.uploadClient)
	if err != nil {
		return PhotoUploadResult{}, err
	}
	if !validUUID(result.AssetID) || result.Inspection.ID != input.InspectionID || len(result.SHA256) != 64 || !lowerHex(result.SHA256) {
		return PhotoUploadResult{}, errors.New("data-api: invalid photo upload result")
	}
	return result, nil
}

func lowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
