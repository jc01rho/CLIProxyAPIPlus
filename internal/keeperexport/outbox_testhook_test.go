package keeperexport

import "context"

func setBeforeAckCommitTestHook(outbox *Outbox, hook func(context.Context) error) {
	outbox.mu.Lock()
	outbox.testHooks.beforeAckCommit = hook
	outbox.mu.Unlock()
}
