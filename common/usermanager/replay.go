package usermanager

import (
	"cmp"
	"slices"

	"github.com/sagernet/sing-box/adapter"
)

// DefaultReplayCacheSize is the standard bounded replay-cache capacity used
// when Options.ReplayCacheSize is zero.
const DefaultReplayCacheSize = 64

const (
	fingerprintOffset64 uint64 = 14695981039346656037
	fingerprintPrime64  uint64 = 1099511628211
)

const (
	fingerprintDomainTransaction byte = iota + 1
	fingerprintDomainReplacement
)

type replayEntry struct {
	fingerprint uint64
	result      adapter.UserTransactionResult
}

type replayCache struct {
	capacity int
	entries  map[string]replayEntry
	order    []string
	head     int
	size     int
}

func newReplayCache(capacity int) replayCache {
	if capacity <= 0 {
		return replayCache{}
	}
	return replayCache{
		capacity: capacity,
		entries:  make(map[string]replayEntry, capacity),
		order:    make([]string, capacity),
	}
}

func (c *replayCache) get(requestID string) (replayEntry, bool) {
	if c.capacity == 0 {
		return replayEntry{}, false
	}
	entry, loaded := c.entries[requestID]
	if !loaded {
		return replayEntry{}, false
	}
	entry.result = cloneTransactionResult(entry.result)
	return entry, true
}

func (c *replayCache) put(requestID string, fingerprint uint64, result adapter.UserTransactionResult) {
	if c.capacity == 0 {
		return
	}
	entry := replayEntry{
		fingerprint: fingerprint,
		result:      cloneTransactionResult(result),
	}
	if _, loaded := c.entries[requestID]; loaded {
		c.entries[requestID] = entry
		return
	}
	if c.size < c.capacity {
		index := (c.head + c.size) % c.capacity
		c.order[index] = requestID
		c.size++
	} else {
		evictedRequestID := c.order[c.head]
		delete(c.entries, evictedRequestID)
		c.order[c.head] = requestID
		c.head = (c.head + 1) % c.capacity
	}
	c.entries[requestID] = entry
}

func fingerprintOperations[T any](
	operations []adapter.UserOperation[T],
	fingerprinter Fingerprinter[T],
) uint64 {
	orderedOperations := slices.Clone(operations)
	slices.SortFunc(
		orderedOperations,
		func(left adapter.UserOperation[T], right adapter.UserOperation[T]) int {
			return cmp.Compare(left.ID, right.ID)
		},
	)
	fingerprint := fingerprintByte(fingerprintOffset64, fingerprintDomainTransaction)
	for _, operation := range orderedOperations {
		fingerprint = fingerprintByte(fingerprint, byte(operation.Type))
		fingerprint = fingerprintID(fingerprint, operation.ID)
		switch operation.Type {
		case adapter.UserOperationAdd, adapter.UserOperationUpdate:
			if fingerprinter != nil {
				fingerprint = fingerprintUint64(
					fingerprint,
					fingerprinter.FingerprintUser(operation.Value),
				)
			}
		}
	}
	return fingerprint
}

func fingerprintReplacement[T any](
	ids []adapter.UserID,
	users map[adapter.UserID]T,
	fingerprinter Fingerprinter[T],
) uint64 {
	orderedIDs := slices.Clone(ids)
	slices.Sort(orderedIDs)
	fingerprint := fingerprintByte(fingerprintOffset64, fingerprintDomainReplacement)
	for _, id := range orderedIDs {
		fingerprint = fingerprintByte(fingerprint, byte(adapter.UserOperationUpdate))
		fingerprint = fingerprintID(fingerprint, id)
		if fingerprinter != nil {
			fingerprint = fingerprintUint64(fingerprint, fingerprinter.FingerprintUser(users[id]))
		}
	}
	return fingerprint
}

func fingerprintID(fingerprint uint64, id adapter.UserID) uint64 {
	fingerprint = fingerprintUint64(fingerprint, uint64(len(id)))
	for index := range len(id) {
		fingerprint = fingerprintByte(fingerprint, id[index])
	}
	return fingerprint
}

func fingerprintUint64(fingerprint uint64, value uint64) uint64 {
	for range 8 {
		fingerprint = fingerprintByte(fingerprint, byte(value))
		value >>= 8
	}
	return fingerprint
}

func fingerprintByte(fingerprint uint64, value byte) uint64 {
	return (fingerprint ^ uint64(value)) * fingerprintPrime64
}

func cloneTransactionResult(result adapter.UserTransactionResult) adapter.UserTransactionResult {
	result.Added = slices.Clone(result.Added)
	result.Updated = slices.Clone(result.Updated)
	result.Deleted = slices.Clone(result.Deleted)
	return result
}
