package core

import "sync"

// relayActivity is shared by all session managers of one project. Checking
// idleness and admitting a delegated turn must use the same lock as normal turns.
type relayActivity struct {
	mu       sync.Mutex
	active   int
	reserved bool
}

func (a *relayActivity) start(reserved bool) bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reserved != reserved {
		return false
	}
	a.active++
	return true
}

func (a *relayActivity) finish() {
	a.finishTurn(false)
}

func (a *relayActivity) finishTurn(reserved bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.active--
	if reserved {
		a.reserved = false
	}
	a.mu.Unlock()
}

func (a *relayActivity) isReserved() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reserved
}

func (a *relayActivity) reserve() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != 0 || a.reserved {
		return false
	}
	a.reserved = true
	return true
}

func (a *relayActivity) release() {
	a.mu.Lock()
	a.reserved = false
	a.mu.Unlock()
}

// Teardown can begin during a reserved task. Remain busy until it completes.
func (a *relayActivity) closing() func() {
	if a == nil {
		return func() {}
	}
	a.mu.Lock()
	a.active++
	a.mu.Unlock()
	return a.finish
}
