package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookDispatcherSignsPayload(t *testing.T) {
	n := Notification{
		Kind:  "test",
		Level: LevelInfo,
		Title: "hello",
		Body:  "world",
		At:    time.Unix(1700000000, 0).UTC(),
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	wantSig := signWebhookPayload("secret", "1700000000", payload)

	var gotTimestamp, gotSignature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTimestamp = r.Header.Get("X-Trove-Timestamp")
		gotSignature = r.Header.Get("X-Trove-Signature")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := &webhookDispatcher{client: srv.Client(), url: srv.URL, secret: "secret"}
	if err := d.Send(context.Background(), n); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotTimestamp != "1700000000" {
		t.Fatalf("timestamp = %q, want 1700000000", gotTimestamp)
	}
	if gotSignature != wantSig {
		t.Fatalf("signature = %q, want %q", gotSignature, wantSig)
	}
}

func TestPostClassifiesNonRetryable4xxAsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "payload rejected", http.StatusBadRequest)
	}))
	defer srv.Close()

	err := post(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	})
	if err == nil || !isPermanentDelivery(err) {
		t.Fatalf("post error = %v, want permanent delivery error", err)
	}
}

func TestNtfyRejectsInvalidTitleWithoutSending(t *testing.T) {
	d := &ntfyDispatcher{client: http.DefaultClient, url: "https://ntfy.example/topic"}
	err := d.Send(context.Background(), Notification{Title: "bad\nheader", Body: "body"})
	if err == nil || !isPermanentDelivery(err) {
		t.Fatalf("ntfy error = %v, want permanent delivery error", err)
	}
}

func TestTruncateRunesPreservesUTF8(t *testing.T) {
	if got := truncateRunes("abcd", 3); got != "ab…" {
		t.Fatalf("truncateRunes = %q, want %q", got, "ab…")
	}
	if got := truncateRunes("éclair", 4); got != "écl…" {
		t.Fatalf("unicode truncateRunes = %q", got)
	}
}
