package main

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
)

func TestAttentionNotifyConfigKeepsDefaultsWhenDisabled(t *testing.T) {
	f := false
	for _, cfg := range []config.AttentionNotifyConfig{{}, {Enabled: &f}} {
		got := attentionNotifyConfig(cfg)
		if got.Enabled || !got.OnTurnComplete || !got.OnBlocked || !got.OnError || !got.MentionUser {
			t.Fatalf("disabled config must retain options for /notify on: %+v", got)
		}
	}
}

func TestAttentionNotifyConfigPreservesExplicitOptions(t *testing.T) {
	f := false
	seconds := 30
	got := attentionNotifyConfig(config.AttentionNotifyConfig{
		Enabled: &f, OnTurnComplete: &f, OnBlocked: &f, OnError: &f, MentionUser: &f,
		Content: "custom", MinDurationSecs: &seconds,
	})
	if got.Enabled || got.OnTurnComplete || got.OnBlocked || got.OnError || got.MentionUser || got.Content != "custom" || got.MinDuration != 30*time.Second {
		t.Fatalf("explicit options were lost: %+v", got)
	}
}
