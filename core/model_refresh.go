package core

import (
	"context"
	"errors"
	"time"
)

type refreshedModelCatalog struct {
	Models    []ModelOption `json:"models"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func modelRefreshScope(agent Agent, refresher ModelRefresher) string {
	return modelCatalogScope(agent) + ":" + refresher.ModelCatalogKey()
}

func (ps *ProjectStateStore) refreshedModels(scope string) refreshedModelCatalog {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	catalog := ps.state.RefreshedModels[scope]
	catalog.Models = append([]ModelOption(nil), catalog.Models...)
	return catalog
}

func (ps *ProjectStateStore) saveRefreshedModels(scope string, models []ModelOption) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	old, existed := ps.state.RefreshedModels[scope]
	if ps.state.RefreshedModels == nil {
		ps.state.RefreshedModels = make(map[string]refreshedModelCatalog)
	}
	ps.state.RefreshedModels[scope] = refreshedModelCatalog{Models: append([]ModelOption(nil), models...), UpdatedAt: time.Now().UTC()}
	if err := ps.saveLocked(); err != nil {
		if existed {
			ps.state.RefreshedModels[scope] = old
		} else {
			delete(ps.state.RefreshedModels, scope)
		}
		return err
	}
	return nil
}

func (e *Engine) cmdModelRefresh(p Platform, msg *Message, agent Agent) {
	refresher, ok := agent.(ModelRefresher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModelRefreshUnsupported))
		return
	}
	if !e.modelRefreshMu.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModelRefreshBusy))
		return
	}
	defer e.modelRefreshMu.Unlock()
	scope := modelRefreshScope(agent, refresher)
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	models, err := refresher.RefreshModels(ctx)
	models = mergeModelCatalog(models, nil, "")
	if err == nil && len(models) == 0 {
		err = &ModelRefreshError{Reason: "empty"}
	}
	if err == nil && scope != modelRefreshScope(agent, refresher) {
		err = &ModelRefreshError{Reason: "provider_changed"}
	}
	if err == nil {
		if saveErr := e.modelCatalogStore().saveRefreshedModels(scope, models); saveErr != nil {
			err = &ModelRefreshError{Reason: "save"}
		}
	}
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelRefreshFailed, e.modelRefreshErrorText(err)))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelRefreshDone, len(models)))
	e.cmdModel(p, msg, nil)
}

func (e *Engine) modelRefreshErrorText(err error) string {
	var refreshErr *ModelRefreshError
	if errors.As(err, &refreshErr) {
		if refreshErr.Status != 0 {
			return e.i18n.Tf(MsgModelRefreshHTTP, refreshErr.Status)
		}
		keys := map[string]MsgKey{
			"empty": MsgModelRefreshEmpty, "provider_changed": MsgModelRefreshProviderChanged,
			"save": MsgModelRefreshSave, "timeout": MsgModelRefreshTimeout,
			"invalid_response": MsgModelRefreshInvalidResponse, "invalid_endpoint": MsgModelRefreshInvalidEndpoint,
		}
		if key, ok := keys[refreshErr.Reason]; ok {
			return e.i18n.T(key)
		}
	}
	return e.i18n.T(MsgModelRefreshNetwork)
}
