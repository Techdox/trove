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
