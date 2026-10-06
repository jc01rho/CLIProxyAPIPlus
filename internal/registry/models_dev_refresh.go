package registry

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

var notifyModelsDevRefreshIdle func()

func noteUnknownModelsDevID(ids []string, id string) []string {
	if _, ok := LookupModelsDevLimit(id); ok {
		return ids
	}
	return append(ids, id)
}

func (r *ModelRegistry) SetRefreshUnknownModels(refresh func(context.Context) error) {
	if r == nil {
		return
	}
	r.modelsDevMu.Lock()
	r.refreshUnknownModels = refresh
	r.modelsDevMu.Unlock()
}

func (r *ModelRegistry) scheduleModelsDevRefresh(ids []string) {
	if r == nil || len(ids) == 0 {
		return
	}
	r.modelsDevMu.Lock()
	refresh := r.refreshUnknownModels
	if refresh == nil {
		r.modelsDevMu.Unlock()
		return
	}
	if r.modelsDevRefreshRunning {
		r.modelsDevRefreshPending = true
		r.modelsDevMu.Unlock()
		return
	}
	r.modelsDevRefreshRunning = true
	r.modelsDevMu.Unlock()
	go r.runModelsDevRefresh(refresh)
}

func (r *ModelRegistry) runModelsDevRefresh(refresh func(context.Context) error) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := refresh(ctx)
		cancel()
		if err != nil {
			log.Warnf("models.dev model limits unavailable after a new model registration: %v", err)
		} else {
			r.mutex.Lock()
			r.invalidateAvailableModelsCacheLocked()
			r.mutex.Unlock()
		}

		r.modelsDevMu.Lock()
		if !r.modelsDevRefreshPending {
			r.modelsDevRefreshRunning = false
			r.modelsDevMu.Unlock()
			if notifyModelsDevRefreshIdle != nil {
				notifyModelsDevRefreshIdle()
			}
			return
		}
		r.modelsDevRefreshPending = false
		refresh = r.refreshUnknownModels
		r.modelsDevMu.Unlock()
		if refresh == nil {
			r.modelsDevMu.Lock()
			r.modelsDevRefreshRunning = false
			r.modelsDevMu.Unlock()
			if notifyModelsDevRefreshIdle != nil {
				notifyModelsDevRefreshIdle()
			}
			return
		}
	}
}
