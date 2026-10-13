package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/quota"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
)

type quotaSnapshotSinkFunc func(quota.Snapshot) error

func (f quotaSnapshotSinkFunc) SaveQuotaSnapshot(snapshot quota.Snapshot) error { return f(snapshot) }

func (d *Daemon) persistHealthObservation(observation runtimehealth.HealthObservation) error {
	writeErr := retryPersistenceWrite(func() error { return persistHealthState(d.store, observation.Route, observation.State) })
	if writeErr == nil {
		d.ackSpoolRecord(runtimehealth.HealthSpoolKey(observation))
		return nil
	}
	if d.persistenceSpool == nil {
		return writeErr
	}
	dead, spoolErr := d.persistenceSpool.DeferHealth(observation, 1, writeErr)
	if spoolErr != nil {
		return errors.Join(writeErr, fmt.Errorf("durable health spool: %w", spoolErr))
	}
	d.reportDeferredPersistence("health", runtimehealth.HealthSpoolKey(observation), dead, writeErr)
	return nil
}

func (d *Daemon) persistQuotaSnapshot(snapshot quota.Snapshot) error {
	writeErr := retryPersistenceWrite(func() error { return d.store.SaveQuotaSnapshot(snapshot) })
	if writeErr == nil {
		d.ackSpoolRecord(runtimehealth.QuotaSpoolKey(snapshot))
		return nil
	}
	if d.persistenceSpool == nil {
		return writeErr
	}
	dead, spoolErr := d.persistenceSpool.DeferQuota(snapshot, 1, writeErr)
	if spoolErr != nil {
		return errors.Join(writeErr, fmt.Errorf("durable quota spool: %w", spoolErr))
	}
	d.reportDeferredPersistence("quota", runtimehealth.QuotaSpoolKey(snapshot), dead, writeErr)
	return nil
}

func (d *Daemon) ackSpoolRecord(key string) {
	if d.persistenceSpool == nil {
		return
	}
	if err := d.persistenceSpool.Acknowledge(key); err != nil {
		d.appendLog("warn", fmt.Sprintf("runtime persistence spool acknowledgement failed for %s: %v", key, err))
	}
}

func (d *Daemon) reportDeferredPersistence(kind, key string, deadLetter bool, cause error) {
	level, message := "warn", "temporarily spooled for retry"
	if deadLetter {
		level, message = "error", "moved to durable dead-letter spool after retry limit"
	}
	d.appendLog(level, fmt.Sprintf("%s persistence %s (%s): %v", kind, message, key, cause))
}

func retryPersistenceWrite(write func() error) error {
	delays := [...]time.Duration{10 * time.Millisecond, 50 * time.Millisecond}
	for attempt := 0; ; attempt++ {
		if err := write(); err == nil {
			return nil
		} else if attempt >= len(delays) {
			return err
		} else {
			time.Sleep(delays[attempt])
		}
	}
}

func (d *Daemon) runPersistenceSpoolReplay(ctx context.Context, healthQueue *runtimehealth.HealthObservationQueue, quotaQueue *runtimehealth.QuotaSnapshotQueue) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		d.replayPersistenceSpool(healthQueue, quotaQueue)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.replayPersistenceSpool(healthQueue, quotaQueue)
			}
		}
	}()
	return done
}

func (d *Daemon) replayPersistenceSpool(healthQueue *runtimehealth.HealthObservationQueue, quotaQueue *runtimehealth.QuotaSnapshotQueue) {
	if d.persistenceSpool == nil {
		return
	}
	for _, record := range d.persistenceSpool.Due(time.Now()) {
		switch record.Kind {
		case "health":
			observation, ok := record.HealthObservation()
			if !ok || observation.State.Generation != record.Generation {
				d.ackSpoolRecord(record.Key)
				continue
			}
			current, queued := d.policy.ReplayHealthObservation(observation, healthQueue.Enqueue)
			if !current {
				d.ackSpoolRecord(record.Key)
			}
			_ = queued // false means bounded queue pressure; retain the durable record.
		case "quota":
			snapshot, ok := record.QuotaSnapshot()
			if !ok || !d.policy.Health.QuotaGenerationCurrent(snapshot) {
				d.ackSpoolRecord(record.Key)
				continue
			}
			current := d.policy.RecordQuota(snapshot, func(resolved quota.Snapshot) {
				quotaQueue.Enqueue(resolved)
			})
			if !current {
				d.ackSpoolRecord(record.Key)
			}
		default:
			d.appendLog("error", "runtime persistence spool contained an unsupported record kind")
		}
	}
}
