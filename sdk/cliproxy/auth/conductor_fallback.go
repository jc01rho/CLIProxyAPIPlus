package auth

// GetSelector returns the active credential selector.
func (m *Manager) GetSelector() Selector {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selector
}

func (m *Manager) SetFallbackModels(models map[string]string) {
	if m == nil {
		return
	}
	if models == nil {
		models = map[string]string{}
	}
	m.fallbackModels.Store(models)
}

func (m *Manager) SetFallbackChain(chain []string, maxDepth int) {
	if m == nil {
		return
	}
	if chain == nil {
		chain = []string{}
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	m.fallbackChain.Store(chain)
	m.fallbackMaxDepth.Store(int32(maxDepth))
}
