package mapapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

func signedInitData(token string, fields url.Values) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+fields.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(token))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(lines, "\n")))
	copy := url.Values{}
	for key, values := range fields {
		copy[key] = append([]string(nil), values...)
	}
	copy.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	return copy.Encode()
}

func TestVerifierChecksMAXSignatureActorAndFreshness(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	const token = "synthetic-map-test-token"
	verifier, err := NewVerifier(token, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	fields := url.Values{"auth_date": {fmt.Sprint(now.Unix())}, "user": {`{"id":8000000000000000001,"first_name":"Тест"}`}, "query_id": {"synthetic-session"}}
	raw := signedInitData(token, fields)
	if actor, err := verifier.Actor(raw); err != nil || actor != "8000000000000000001" {
		t.Fatalf("valid signed actor: %q %v", actor, err)
	}
	checks := map[string]string{}
	forged := strings.Replace(raw, "8000000000000000001", "8000000000000000002", 1)
	checks["forged user"] = forged
	checks["duplicate hash"] = raw + "&hash=00"
	checks["duplicate actor"] = raw + "&user=%7B%22id%22%3A1%7D"
	checks["malformed percent"] = raw + "&bad=%ZZ"
	checks["missing hash"] = strings.Replace(raw, "hash=", "removed=", 1)
	for name, value := range checks {
		if actor, err := verifier.Actor(value); err == nil || actor != "" {
			t.Fatalf("%s accepted: actor=%q err=%v", name, actor, err)
		}
	}
	fields.Set("auth_date", fmt.Sprint(now.Add(-time.Hour-time.Second).Unix()))
	if _, err := verifier.Actor(signedInitData(token, fields)); err == nil {
		t.Fatal("expired signed initData accepted")
	}
	fields.Set("auth_date", fmt.Sprint(now.Add(time.Minute+time.Second).Unix()))
	if _, err := verifier.Actor(signedInitData(token, fields)); err == nil {
		t.Fatal("future signed initData accepted")
	}
	fields.Set("auth_date", fmt.Sprint(now.Unix()))
	fields.Set("user", `{"id":0}`)
	if _, err := verifier.Actor(signedInitData(token, fields)); err == nil {
		t.Fatal("signed zero actor accepted")
	}
}
