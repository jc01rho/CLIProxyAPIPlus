package registry

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var clineModelsUpdaterOnce sync.Once

// StartClineModelsUpdater refreshes Cline model registrations immediately and
// every catalog interval. Cline owns its live catalog and free-model feed, so
// no local Cline model list is retained.
func StartClineModelsUpdater(ctx context.Context) {
	clineModelsUpdaterOnce.Do(func() {
		go runClineModelsUpdater(ctx)
	})
}

func runClineModelsUpdater(ctx context.Context) {
	notifyModelRefresh([]string{"cline"})

	ticker := time.NewTicker(modelsRefreshInterval)
	defer ticker.Stop()
	log.Infof("periodic Cline model refresh started (interval=%s)", modelsRefreshInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notifyModelRefresh([]string{"cline"})
		}
	}
}
