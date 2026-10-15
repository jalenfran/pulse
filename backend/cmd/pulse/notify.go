package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jalenfran/pulse/backend/internal/notify"
	"github.com/jalenfran/pulse/backend/internal/store"
)

// Notification settings, all from the environment:
//
//	PULSE_NTFY_URL              ntfy server base URL; unset disables notifications
//	PULSE_NTFY_TOPIC            topic (default "pulse")
//	PULSE_NTFY_TOKEN            ntfy access token with write access to the topic
//	PULSE_NOTIFY_MIN_SEVERITY   info | warning | critical (default warning)
//	PULSE_NOTIFY_ALWAYS         kinds that notify at any severity (default nixos-upgrade,node-ready)
//	PULSE_NOTIFY_IGNORE         kinds that never notify (default k8s-event)
//	PULSE_NOTIFY_COOLDOWN       per-key cooldown (default 6h)
//	PULSE_QUIET_HOURS           local hours "23-8"; "off" disables (default 23-8)
//	TZ                          time zone for quiet hours and digests

func ntfyFromEnv() *notify.Ntfy {
	u := os.Getenv("PULSE_NTFY_URL")
	if u == "" {
		return nil
	}
	return &notify.Ntfy{
		URL:    u,
		Topic:  env("PULSE_NTFY_TOPIC", "pulse"),
		Token:  os.Getenv("PULSE_NTFY_TOKEN"),
		Client: &http.Client{Timeout: 15 * time.Second},
	}
}

func notifierFromEnv(st *store.Store, log *slog.Logger) (*notify.Notifier, error) {
	sender := ntfyFromEnv()
	if sender == nil {
		return nil, nil
	}
	rules := notify.Rules{
		Always: set(env("PULSE_NOTIFY_ALWAYS", "nixos-upgrade,node-ready")),
		Ignore: set(env("PULSE_NOTIFY_IGNORE", "k8s-event")),
		MaxAge: time.Hour,
	}
	var ok bool
	if rules.MinSeverity, ok = store.SeverityRank(env("PULSE_NOTIFY_MIN_SEVERITY", "warning")); !ok {
		return nil, fmt.Errorf("PULSE_NOTIFY_MIN_SEVERITY must be info, warning or critical")
	}
	var err error
	if rules.Cooldown, err = time.ParseDuration(env("PULSE_NOTIFY_COOLDOWN", "6h")); err != nil {
		return nil, fmt.Errorf("PULSE_NOTIFY_COOLDOWN: %w", err)
	}
	if rules.QuietStart, rules.QuietEnd, err = quietHours(env("PULSE_QUIET_HOURS", "23-8")); err != nil {
		return nil, err
	}
	log.Info("notifications enabled", "ntfy", sender.URL, "topic", sender.Topic,
		"quiet", env("PULSE_QUIET_HOURS", "23-8"), "tz", time.Local.String())
	return notify.New(st, sender, rules, log), nil
}

func quietHours(v string) (start, end int, err error) {
	if v == "off" {
		return 0, 0, nil
	}
	a, b, found := strings.Cut(v, "-")
	start, err1 := strconv.Atoi(a)
	end, err2 := strconv.Atoi(b)
	if !found || err1 != nil || err2 != nil || start < 0 || start > 23 || end < 0 || end > 23 {
		return 0, 0, fmt.Errorf(`PULSE_QUIET_HOURS must look like "23-8" or be "off"`)
	}
	return start, end, nil
}

func set(csv string) map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Split(csv, ",") {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	return m
}

func notifyCmd(args []string) error {
	if len(args) == 0 || args[0] != "test" {
		usage()
	}
	sender := ntfyFromEnv()
	if sender == nil {
		return fmt.Errorf("PULSE_NTFY_URL is not set")
	}
	msg := "If you can read this, notifications work."
	if len(args) > 1 {
		msg = strings.Join(args[1:], " ")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sender.Send(ctx, notify.Message{Title: "Pulse test", Body: msg, Severity: "info", Kind: "test", Click: "pulse://events"}); err != nil {
		return err
	}
	fmt.Printf("Sent to %s/%s.\n", strings.TrimRight(sender.URL, "/"), sender.Topic)
	return nil
}
