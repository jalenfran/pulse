package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNtfyPayload(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		got = nil // a fresh map per request: decoding into the old one would merge
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	s := &Ntfy{URL: srv.URL, Topic: "pulse", Token: "tk_test"}
	err := s.Send(context.Background(), Message{Title: "node2 is NotReady", Severity: "warning",
		Kind: "node-notready", Click: "pulse://events/01JABCDEFGHJKMNPQRSTVWXYZ0"})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tk_test" {
		t.Errorf("Authorization = %q", auth)
	}
	want := map[string]any{
		"topic":    "pulse",
		"title":    "node2 is NotReady",
		"message":  "node2 is NotReady", // empty body falls back to the title
		"priority": float64(4),
		"click":    "pulse://events/01JABCDEFGHJKMNPQRSTVWXYZ0",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}

	// No click link means no click field, so ntfy just opens its own app.
	if err := s.Send(context.Background(), Message{Title: "t", Severity: "info"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["click"]; ok {
		t.Errorf("click present without a link: %v", got["click"])
	}
}
