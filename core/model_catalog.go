package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Each project owns its store. Agent/provider names isolate catalogs without
// storing endpoints or credentials; a missing provider has a distinct key.
func modelCatalogScope(agent Agent) string {
	parts := []string{agent.Name()}
	if providers, ok := agent.(ProviderSwitcher); ok {
		if active := providers.GetActiveProvider(); active != nil {
			parts = append(parts, active.Name)
		}
	}
	key, _ := json.Marshal(parts)
	return string(key)
}

func (e *Engine) modelCatalogStore() *ProjectStateStore {
	if e.projectState != nil {
		return e.projectState
	}
	return e.transientModelCatalog
}

func (ps *ProjectStateStore) rememberedModels(scope string) []string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return append([]string(nil), ps.state.RememberedModels[scope]...)
}

// Save before publishing a selection. If its separate config write fails,
// roll back the registration while holding the store lock so concurrent model
// switches cannot lose each other's additions. apply must not re-enter ps.
func (ps *ProjectStateStore) withRememberedModel(scope, model string, apply func() error) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	previous, existed := ps.state.RememberedModels[scope]
	for _, known := range previous {
		if strings.EqualFold(known, model) {
			return apply()
		}
	}
	if ps.state.RememberedModels == nil {
		ps.state.RememberedModels = make(map[string][]string)
	}
	added := append(append([]string(nil), previous...), model)
	ps.state.RememberedModels[scope] = added
	restore := func() {
		if existed {
			ps.state.RememberedModels[scope] = previous
		} else {
			delete(ps.state.RememberedModels, scope)
		}
	}
	if err := ps.saveLocked(); err != nil {
		restore()
		return fmt.Errorf("save model catalog: %w", err)
	}
	if err := apply(); err != nil {
		restore()
		if rollbackErr := ps.saveLocked(); rollbackErr != nil {
			ps.state.RememberedModels[scope] = added
			return fmt.Errorf("%w; model catalog rollback failed: %v", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (e *Engine) availableModels(ctx context.Context, agent Agent) []ModelOption {
	switcher, ok := agent.(ModelSwitcher)
	if !ok {
		return nil
	}
	discovered := switcher.AvailableModels(ctx)
	var remembered []string
	if store := e.modelCatalogStore(); store != nil {
		remembered = store.rememberedModels(modelCatalogScope(agent))
	}
	return mergeModelCatalog(discovered, remembered, switcher.GetModel())
}

// Preserve discovery order, aliases and descriptions, then append remembered
// models and the current selection. Numbered choices use this same list.
func mergeModelCatalog(discovered []ModelOption, remembered []string, current string) []ModelOption {
	result := make([]ModelOption, 0, len(discovered)+len(remembered)+1)
	seen := make(map[string]bool)
	add := func(model ModelOption) {
		model.Name = strings.TrimSpace(model.Name)
		key := strings.ToLower(model.Name)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		result = append(result, model)
	}
	for _, model := range discovered {
		add(model)
	}
	for _, name := range remembered {
		add(ModelOption{Name: name})
	}
	add(ModelOption{Name: current})
	return result
}

func (e *Engine) switchModelOnAgent(agent Agent, target string, persistConfig bool) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("model name is empty")
	}
	if _, ok := agent.(ModelSwitcher); !ok {
		return target, nil
	}
	resolved := target
	apply := func() error {
		var err error
		resolved, err = e.applyModelOnAgent(agent, target, persistConfig)
		return err
	}
	if store := e.modelCatalogStore(); store != nil {
		if err := store.withRememberedModel(modelCatalogScope(agent), target, apply); err != nil {
			return "", err
		}
	} else if err := apply(); err != nil {
		return "", err
	}
	return resolved, nil
}
