package dataapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ContractVersion = "1.0"
	maxJSONBytes    = 2 << 20
	maxAttempts     = 4 // initial request and at most three retries
)

type Config struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type Client struct {
	baseURL      *url.URL
	token        string
	httpClient   *http.Client
	uploadClient *http.Client
	wait         func(context.Context, time.Duration) error
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "/internal/v1" {
		return nil, errors.New("data-api: DATA_API_BASE_URL must end in /internal/v1 and use http(s)")
	}
	if cfg.Token == "" || strings.ContainsAny(cfg.Token, "\r\n") {
		return nil, errors.New("data-api: DATA_API_TOKEN is missing or invalid")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext},
		}
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	uploadClient := *httpClient
	uploadClient.Timeout = 60 * time.Second
	return &Client{baseURL: u, token: cfg.Token, httpClient: httpClient, uploadClient: &uploadClient, wait: waitContext}, nil
}

func (c *Client) Meta(ctx context.Context) (Meta, error) {
	meta, err := get[Meta](ctx, c, "/meta", "")
	if err != nil {
		return Meta{}, err
	}
	if meta.ContractVersion != ContractVersion || (meta.Mode != "mock" && meta.Mode != "real") {
		return Meta{}, errors.New("data-api: incompatible metadata")
	}
	return meta, nil
}

func (c *Client) Me(ctx context.Context, actorMaxID string) (Me, error) {
	return get[Me](ctx, c, "/me", actorMaxID)
}

func (c *Client) CurrentRules(ctx context.Context, actorMaxID string) (Rules, error) {
	return get[Rules](ctx, c, "/rules/current", actorMaxID)
}

type VehicleFilter struct {
	Available *bool
	Limit     int
	Cursor    string
}

func (c *Client) Vehicles(ctx context.Context, actorMaxID string, filter VehicleFilter) (Page[Vehicle], error) {
	if filter.Limit < 0 || filter.Limit > 50 {
		return Page[Vehicle]{}, errors.New("data-api: limit must be 1..50 or omitted")
	}
	query := url.Values{}
	if filter.Available != nil {
		query.Set("available", strconv.FormatBool(*filter.Available))
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Cursor != "" {
		query.Set("cursor", filter.Cursor)
	}
	path := "/vehicles"
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	return get[Page[Vehicle]](ctx, c, path, actorMaxID)
}

func (c *Client) Vehicle(ctx context.Context, actorMaxID, vehicleID string) (Vehicle, error) {
	if !validUUID(vehicleID) {
		return Vehicle{}, errors.New("data-api: invalid vehicle ID")
	}
	return get[Vehicle](ctx, c, "/vehicles/"+vehicleID, actorMaxID)
}

func (c *Client) PreviousInspection(ctx context.Context, actorMaxID, vehicleID string) (Inspection, error) {
	if !validUUID(vehicleID) {
		return Inspection{}, errors.New("data-api: invalid vehicle ID")
	}
	return get[Inspection](ctx, c, "/vehicles/"+vehicleID+"/previous-inspection", actorMaxID)
}

func (c *Client) State(ctx context.Context, actorMaxID string) (CurrentState, error) {
	return get[CurrentState](ctx, c, "/state", actorMaxID)
}

func (c *Client) Checkout(ctx context.Context, actorMaxID, checkoutID string) (Checkout, error) {
	if !validUUID(checkoutID) {
		return Checkout{}, errors.New("data-api: invalid checkout ID")
	}
	return get[Checkout](ctx, c, "/checkouts/"+checkoutID, actorMaxID)
}

func (c *Client) Inspection(ctx context.Context, actorMaxID, inspectionID string) (Inspection, error) {
	if !validUUID(inspectionID) {
		return Inspection{}, errors.New("data-api: invalid inspection ID")
	}
	return get[Inspection](ctx, c, "/inspections/"+inspectionID, actorMaxID)
}

func (c *Client) Trip(ctx context.Context, actorMaxID, tripID string) (Trip, error) {
	if !validUUID(tripID) {
		return Trip{}, errors.New("data-api: invalid trip ID")
	}
	return get[Trip](ctx, c, "/trips/"+tripID, actorMaxID)
}

func (c *Client) MyTrips(ctx context.Context, actorMaxID string, limit int, cursor string) (Page[Trip], error) {
	if limit < 0 || limit > 50 || len(cursor) > 2048 {
		return Page[Trip]{}, errors.New("data-api: invalid trips page")
	}
	query := url.Values{"scope": {"mine"}}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	return get[Page[Trip]](ctx, c, "/trips?"+query.Encode(), actorMaxID)
}

func (c *Client) AdminSummary(ctx context.Context, actorMaxID string) (AdminSummary, error) {
	return get[AdminSummary](ctx, c, "/admin/summary", actorMaxID)
}

func (c *Client) AdminEmployees(ctx context.Context, actorMaxID string, limit int, cursor string) (Page[Employee], error) {
	if limit < 0 || limit > 50 || len(cursor) > 2048 {
		return Page[Employee]{}, errors.New("data-api: invalid employees page")
	}
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	path := "/admin/employees"
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	return get[Page[Employee]](ctx, c, path, actorMaxID)
}

func (c *Client) AdminEmployee(ctx context.Context, actorMaxID, employeeID string) (Employee, error) {
	if !validUUID(employeeID) {
		return Employee{}, errors.New("data-api: invalid employee ID")
	}
	return get[Employee](ctx, c, "/admin/employees/"+employeeID, actorMaxID)
}

type AdminTripFilter struct {
	State      string
	EmployeeID string
	VehicleID  string
	Limit      int
	Cursor     string
}

func (c *Client) AdminTrips(ctx context.Context, actorMaxID string, filter AdminTripFilter) (Page[Trip], error) {
	if filter.State != "" && filter.State != "active" && filter.State != "returning" && filter.State != "completed" && filter.State != "closed_by_admin" || filter.EmployeeID != "" && !validUUID(filter.EmployeeID) || filter.VehicleID != "" && !validUUID(filter.VehicleID) || filter.Limit < 0 || filter.Limit > 50 || len(filter.Cursor) > 2048 {
		return Page[Trip]{}, errors.New("data-api: invalid admin trips filter")
	}
	query := url.Values{}
	if filter.State != "" {
		query.Set("state", filter.State)
	}
	if filter.EmployeeID != "" {
		query.Set("employee_id", filter.EmployeeID)
	}
	if filter.VehicleID != "" {
		query.Set("vehicle_id", filter.VehicleID)
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Cursor != "" {
		query.Set("cursor", filter.Cursor)
	}
	path := "/admin/trips"
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	return get[Page[Trip]](ctx, c, path, actorMaxID)
}

func (c *Client) Return(ctx context.Context, actorMaxID, returnID string) (Return, error) {
	if !validUUID(returnID) {
		return Return{}, errors.New("data-api: invalid return ID")
	}
	return get[Return](ctx, c, "/returns/"+returnID, actorMaxID)
}

func (c *Client) Issue(ctx context.Context, actorMaxID, issueID string) (Issue, error) {
	if !validUUID(issueID) {
		return Issue{}, errors.New("data-api: invalid issue ID")
	}
	return get[Issue](ctx, c, "/issues/"+issueID, actorMaxID)
}

func get[T any](ctx context.Context, c *Client, path, actorMaxID string) (T, error) {
	return request[T](ctx, c, http.MethodGet, path, actorMaxID, nil, "", "", nil, nil)
}

func request[T any](ctx context.Context, c *Client, method, path, actorMaxID string, body []byte, contentType, idempotencyKey string, inbox *InboxLease, client *http.Client) (T, error) {
	var zero T
	if client == nil {
		client = c.httpClient
	}
	if actorMaxID != "" && !validMaxID(actorMaxID) {
		return zero, errors.New("data-api: invalid actor MAX ID")
	}
	if path != "/meta" && actorMaxID == "" {
		return zero, errors.New("data-api: actor MAX ID required")
	}
	requestID, err := newUUID()
	if err != nil {
		return zero, err
	}
	u := *c.baseURL
	parts := strings.SplitN(path, "?", 2)
	u.Path += parts[0]
	if len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return zero, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("X-Contract-Version", ContractVersion)
		req.Header.Set("X-Request-ID", requestID)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		if inbox != nil {
			req.Header.Set("X-Inbox-Event-ID", inbox.EventID)
			req.Header.Set("X-Inbox-Lease", inbox.Token)
		}
		if actorMaxID != "" {
			req.Header.Set("X-Actor-Max-ID", actorMaxID)
		}
		res, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			if attempt == maxAttempts-1 {
				return zero, fmt.Errorf("data-api: transport failed after %d attempts: %w", maxAttempts, err)
			}
			if err := c.wait(ctx, backoff(attempt, "")); err != nil {
				return zero, err
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, maxJSONBytes+1))
		_ = res.Body.Close()
		if readErr != nil || len(body) > maxJSONBytes {
			return zero, errors.New("data-api: response read failed or exceeds limit")
		}
		if res.StatusCode == http.StatusOK {
			var envelope struct {
				Data      T      `json:"data"`
				RequestID string `json:"request_id"`
			}
			if err := decodeStrict(body, &envelope); err != nil || envelope.RequestID == "" {
				return zero, errors.New("data-api: invalid success response")
			}
			return envelope.Data, nil
		}
		var envelope struct {
			Error     APIError `json:"error"`
			RequestID string   `json:"request_id"`
		}
		if err := decodeStrict(body, &envelope); err != nil || envelope.Error.Code == "" || envelope.RequestID == "" {
			return zero, fmt.Errorf("data-api: invalid HTTP %d error response", res.StatusCode)
		}
		envelope.Error.Status = res.StatusCode
		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusServiceUnavailable) && attempt < maxAttempts-1 {
			if err := c.wait(ctx, backoff(attempt, res.Header.Get("Retry-After"))); err != nil {
				return zero, err
			}
			continue
		}
		return zero, &envelope.Error
	}
	return zero, errors.New("data-api: retry limit reached")
}

func decodeStrict(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, 3*time.Second)
	}
	if date, err := http.ParseTime(retryAfter); err == nil {
		return min(max(time.Until(date), 0), 3*time.Second)
	}
	var jitter [1]byte
	_, _ = rand.Read(jitter[:])
	return time.Duration(100*(1<<attempt)+int(jitter[0])%100) * time.Millisecond
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

func validMaxID(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return id != "0"
}

func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
