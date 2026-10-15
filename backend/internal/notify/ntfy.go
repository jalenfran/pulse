package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Ntfy publishes to an ntfy server (https://ntfy.sh, or self-hosted).
type Ntfy struct {
	URL    string // server base URL, e.g. http://ntfy.ntfy.svc.cluster.local:8080
	Topic  string
	Token  string // access token with write permission on Topic; optional
	Client *http.Client
}

// ntfy turns messages over 4096 bytes into attachments. Stay well under.
const maxNtfyMessage = 3500

func (s *Ntfy) Send(ctx context.Context, m Message) error {
	body := m.Body
	if body == "" {
		body = m.Title
	}
	if len(body) > maxNtfyMessage {
		cut := maxNtfyMessage
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + "…"
	}
	// JSON publishing, rather than headers, so titles needn't be ASCII.
	payload, err := json.Marshal(map[string]any{
		"topic":    s.Topic,
		"title":    m.Title,
		"message":  body,
		"priority": priority(m.Severity),
		"tags":     tags(m),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+"/", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ntfy: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func priority(severity string) int {
	switch severity {
	case "critical":
		return 5
	case "warning":
		return 4
	}
	return 3
}

// tags become emoji in the ntfy apps.
func tags(m Message) []string {
	switch {
	case m.Severity == "critical":
		return []string{"rotating_light"}
	case m.Severity == "warning":
		return []string{"warning"}
	case m.Kind == "nixos-upgrade" || m.Kind == "node-ready":
		return []string{"white_check_mark"}
	case m.Kind == "digest":
		return []string{"clipboard"}
	}
	return []string{"information_source"}
}
