package teamshare

var defaultEngine = NewEngine(nil)

// Default returns the process-wide engine shared by routing, enforcement and the management API.
func Default() *Engine { return defaultEngine }
