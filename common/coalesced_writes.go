package common

import (
	"errors"
	"sync"
)

// CoalescedWrites lets callers combine concurrent updates to the same durable
// observation. Each key has one write in progress and at most one pending
// batch. Callers wait for their own commit, with no timer or worker goroutine.
type CoalescedWrites[K comparable, V any] struct {
	mu    sync.Mutex
	tails map[K]*coalescedBatch[V]
}

type coalescedBatch[V any] struct {
	value   V
	writing bool
	done    chan struct{}
	err     error
}

// Write merges values in arrival order. A failed predecessor is merged into
// the waiting batch before its write, preserving its newer observations.
func (q *CoalescedWrites[K, V]) Write(key K, value V, merge func(V, V) V, persist func(V) error) error {
	q.mu.Lock()
	previous := q.tails[key]
	if previous != nil && !previous.writing {
		previous.value = merge(previous.value, value)
		q.mu.Unlock()
		<-previous.done
		return previous.err
	}
	batch := &coalescedBatch[V]{value: value, writing: previous == nil, done: make(chan struct{})}
	if q.tails == nil {
		q.tails = make(map[K]*coalescedBatch[V])
	}
	q.tails[key] = batch
	q.mu.Unlock()

	if previous != nil {
		<-previous.done
		q.mu.Lock()
		batch.writing = true
		if previous.err != nil {
			batch.value = merge(previous.value, batch.value)
		}
		q.mu.Unlock()
	}
	// If the database driver panics, let the caller's recovery run while
	// still waking peers and allowing the next observation to retry.
	defer func() {
		q.mu.Lock()
		if q.tails[key] == batch {
			delete(q.tails, key)
		}
		close(batch.done)
		q.mu.Unlock()
	}()
	batch.err = errObservationWriteInterrupted
	batch.err = persist(batch.value)
	return batch.err
}

var errObservationWriteInterrupted = errors.New("observation write interrupted")
