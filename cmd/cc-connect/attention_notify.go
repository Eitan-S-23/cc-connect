package main

import (
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func attentionNotifyConfig(cfg config.AttentionNotifyConfig) core.AttentionNotifyCfg {
	an := core.DefaultAttentionNotifyCfg()
	if cfg.Enabled != nil {
		an.Enabled = *cfg.Enabled
	}
	if cfg.OnTurnComplete != nil {
		an.OnTurnComplete = *cfg.OnTurnComplete
	}
	if cfg.OnBlocked != nil {
		an.OnBlocked = *cfg.OnBlocked
	}
	if cfg.OnError != nil {
		an.OnError = *cfg.OnError
	}
	if cfg.MentionUser != nil {
		an.MentionUser = *cfg.MentionUser
	}
	an.Content = cfg.Content
	if cfg.MinDurationSecs != nil && *cfg.MinDurationSecs > 0 {
		an.MinDuration = time.Duration(*cfg.MinDurationSecs) * time.Second
	}
	return an
}
