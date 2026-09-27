package dataapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type AssetContent struct {
	ContentType string
	Bytes       []byte
}

// AssetContent retrieves a private image using the actor's identity.
func (c *Client) AssetContent(ctx context.Context, actorMaxID, assetID string) (AssetContent, error) {
	if !validMaxID(actorMaxID) || !validUUID(assetID) {
		return AssetContent{}, errors.New("data-api: invalid asset request")
	}
	requestID, err := newUUID()
	if err != nil {
		return AssetContent{}, err
	}
	u := *c.baseURL
	u.Path += "/assets/" + assetID + "/content"
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return AssetContent{}, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("X-Contract-Version", ContractVersion)
		req.Header.Set("X-Request-ID", requestID)
		req.Header.Set("X-Actor-Max-ID", actorMaxID)
		req.Header.Set("Accept", "image/jpeg, image/png, image/webp")
		res, err := c.uploadClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return AssetContent{}, ctx.Err()
			}
			if attempt == maxAttempts-1 {
				return AssetContent{}, fmt.Errorf("data-api: asset transport failed: %w", err)
			}
			if err := c.wait(ctx, backoff(attempt, "")); err != nil {
				return AssetContent{}, err
			}
			continue
		}
		limit := int64(10 << 20)
		if res.StatusCode != http.StatusOK {
			limit = maxJSONBytes
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, limit+1))
		_ = res.Body.Close()
		if readErr != nil || int64(len(body)) > limit {
			return AssetContent{}, errors.New("data-api: asset response read failed or exceeds limit")
		}
		if res.StatusCode == http.StatusOK {
			media := res.Header.Get("Content-Type")
			if len(body) == 0 || (media != "image/jpeg" && media != "image/png" && media != "image/webp") {
				return AssetContent{}, errors.New("data-api: invalid asset content")
			}
			return AssetContent{ContentType: media, Bytes: body}, nil
		}
		var envelope struct {
			Error     APIError `json:"error"`
			RequestID string   `json:"request_id"`
		}
		if decodeStrict(body, &envelope) != nil || envelope.Error.Code == "" || envelope.RequestID == "" {
			return AssetContent{}, fmt.Errorf("data-api: invalid asset HTTP %d error", res.StatusCode)
		}
		envelope.Error.Status = res.StatusCode
		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusServiceUnavailable) && attempt < maxAttempts-1 {
			if err := c.wait(ctx, backoff(attempt, res.Header.Get("Retry-After"))); err != nil {
				return AssetContent{}, err
			}
			continue
		}
		return AssetContent{}, &envelope.Error
	}
	return AssetContent{}, errors.New("data-api: asset retry limit reached")
}
