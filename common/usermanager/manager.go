package usermanager

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
)

// Options configures a Manager.
type Options struct {
	// ReplayCacheSize controls the bounded FIFO replay cache. Zero selects
	// DefaultReplayCacheSize, a negative value disables the cache, and a
	// positive value sets an explicit capacity.
	ReplayCacheSize int
}

// Manager serializes typed user updates and owns their canonical state.
// The zero value is invalid; construct one with New.
type Manager[T any] struct {
	updateMutex     sync.Mutex
	canonical       map[adapter.UserID]T
	generation      atomic.Uint64
	backend         Backend[T]
	fingerprinter   Fingerprinter[T]
	replay          replayCache
	postCommitHooks []func(adapter.UserTransactionResult)
}

type replacementState[T any] struct {
	users      map[adapter.UserID]T
	orderedIDs []adapter.UserID
}

// New creates an empty managed-user manager at generation zero.
// It panics if backend is nil.
func New[T any](backend Backend[T], options Options) *Manager[T] {
	if backend == nil {
		panic("usermanager: nil backend")
	}
	replayCacheSize := options.ReplayCacheSize
	if replayCacheSize == 0 {
		replayCacheSize = DefaultReplayCacheSize
	}
	fingerprinter, _ := backend.(Fingerprinter[T])
	return &Manager[T]{
		canonical:       make(map[adapter.UserID]T),
		backend:         backend,
		fingerprinter:   fingerprinter,
		replay:          newReplayCache(replayCacheSize),
		postCommitHooks: make([]func(adapter.UserTransactionResult), 0),
	}
}

// Generation returns the current runtime generation without taking the update lock.
func (m *Manager[T]) Generation() adapter.UserGeneration {
	return adapter.UserGeneration(m.generation.Load())
}

// AddPostCommitHook registers a hook for future successful commits. Hooks run
// under the update lock and must not block, perform I/O, or call ApplyUsers,
// ReplaceUsers, or AddPostCommitHook. A hook panic is degraded coordination
// after publication and cannot roll back authentication state, canonical state,
// or generation; the manager recovers it and continues running later hooks.
func (m *Manager[T]) AddPostCommitHook(hook func(adapter.UserTransactionResult)) {
	if hook == nil {
		return
	}
	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()
	m.postCommitHooks = append(m.postCommitHooks, hook)
}

// ApplyUsers atomically applies a typed delta to future authentication state.
// Context cancellation is not honored: the commit path is non-blocking by
// construction and must not be interrupted mid-transaction.
func (m *Manager[T]) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[T],
) (adapter.UserTransactionResult, error) {
	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()

	previousGeneration := adapter.UserGeneration(m.generation.Load())
	if err := validateExpectedGeneration(transaction.ExpectedGeneration, previousGeneration); err != nil {
		return adapter.UserTransactionResult{}, err
	}
	var fingerprint uint64
	if transaction.RequestID != "" {
		fingerprint = fingerprintOperations(transaction.Operations, m.fingerprinter)
	}
	return m.applyUsersLocked(
		transaction,
		previousGeneration,
		fingerprint,
		nil,
	)
}

// ReplaceUsers reconciles the complete typed user set through the same commit
// path as ApplyUsers. Context cancellation is not honored: the commit path is
// non-blocking by construction and must not be interrupted mid-transaction.
// Every retained ID is reported as updated, even when its value is unchanged,
// and every successful replacement advances the generation.
func (m *Manager[T]) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []T,
) (adapter.UserTransactionResult, error) {
	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()

	previousGeneration := adapter.UserGeneration(m.generation.Load())
	if err := validateExpectedGeneration(expectedGeneration, previousGeneration); err != nil {
		return adapter.UserTransactionResult{}, err
	}
	replacement, err := m.prepareReplacement(users)
	if err != nil {
		return adapter.UserTransactionResult{}, err
	}
	transaction := adapter.UserTransaction[T]{
		ExpectedGeneration: expectedGeneration,
		RequestID:          requestID,
		SourceRevision:     sourceRevision,
	}
	var fingerprint uint64
	if requestID != "" {
		fingerprint = fingerprintReplacement(replacement.orderedIDs, replacement.users, m.fingerprinter)
	}
	return m.applyUsersLocked(
		transaction,
		previousGeneration,
		fingerprint,
		replacement,
	)
}

func validateExpectedGeneration(
	expected adapter.UserGeneration,
	current adapter.UserGeneration,
) error {
	if expected == 0 || expected == current {
		return nil
	}
	return E.Cause(
		ErrGenerationConflict,
		"expected generation ",
		uint64(expected),
		", current generation ",
		uint64(current),
	)
}

func (m *Manager[T]) applyUsersLocked(
	transaction adapter.UserTransaction[T],
	previousGeneration adapter.UserGeneration,
	fingerprint uint64,
	replacement *replacementState[T],
) (adapter.UserTransactionResult, error) {
	if transaction.RequestID != "" {
		cached, loaded := m.replay.get(transaction.RequestID)
		if loaded {
			if m.fingerprinter == nil && transactionCarriesValues(transaction.Operations, replacement) {
				return adapter.UserTransactionResult{}, E.Cause(
					ErrRequestIDConflict,
					"request ID ",
					transaction.RequestID,
					": backend does not implement Fingerprinter, so value-carrying replay cannot be proven identical",
				)
			}
			if cached.fingerprint != fingerprint {
				return adapter.UserTransactionResult{}, E.Cause(
					ErrRequestIDConflict,
					"request ID ",
					transaction.RequestID,
				)
			}
			if cached.result.Generation != previousGeneration {
				return adapter.UserTransactionResult{}, E.Cause(
					ErrRequestIDConflict,
					"request ID ",
					transaction.RequestID,
					": canonical state advanced since the original commit from generation ",
					uint64(cached.result.Generation),
					" to ",
					uint64(previousGeneration),
				)
			}
			cached.result.Replayed = true
			return cached.result, nil
		}
	}

	if replacement != nil {
		transaction.Operations = m.replacementOperations(replacement)
	}
	if err := validateOperations(transaction.Operations); err != nil {
		return adapter.UserTransactionResult{}, err
	}

	next := maps.Clone(m.canonical)
	if err := applyOperations(next, transaction.Operations); err != nil {
		return adapter.UserTransactionResult{}, err
	}

	sortedIDs, err := m.validateStableIDs(next)
	if err != nil {
		return adapter.UserTransactionResult{}, err
	}
	records := recordsByID(next, sortedIDs)
	published, err := m.backend.Prepare(records)
	if err != nil {
		return adapter.UserTransactionResult{}, wrapBackendPrepareError(err)
	}
	if published == nil {
		return adapter.UserTransactionResult{}, E.Cause(
			ErrBackendPrepareFailure,
			"prepare returned nil published state",
		)
	}

	published.Commit()
	m.canonical = next
	generation := adapter.UserGeneration(m.generation.Add(1))
	result := buildTransactionResult(previousGeneration, generation, transaction.Operations)
	if transaction.RequestID != "" {
		m.replay.put(transaction.RequestID, fingerprint, result)
	}
	m.runPostCommitHooks(result)
	return result, nil
}

func transactionCarriesValues[T any](
	operations []adapter.UserOperation[T],
	replacement *replacementState[T],
) bool {
	if replacement != nil {
		return len(replacement.orderedIDs) != 0
	}
	for _, operation := range operations {
		switch operation.Type {
		case adapter.UserOperationAdd, adapter.UserOperationUpdate:
			return true
		}
	}
	return false
}

func validateOperations[T any](operations []adapter.UserOperation[T]) error {
	if len(operations) == 0 {
		return nil
	}
	seen := make(map[adapter.UserID]struct{}, len(operations))
	for index, operation := range operations {
		if operation.ID == "" {
			return E.Cause(ErrEmptyUserID, "operation ", index)
		}
		if _, loaded := seen[operation.ID]; loaded {
			return E.Cause(
				ErrDuplicateUserID,
				"operation ",
				index,
				" repeats stable ID ",
				string(operation.ID),
			)
		}
		seen[operation.ID] = struct{}{}
		switch operation.Type {
		case adapter.UserOperationAdd, adapter.UserOperationUpdate, adapter.UserOperationDelete:
		default:
			return E.Cause(
				ErrInvalidTransaction,
				"operation ",
				index,
				" has invalid type ",
				uint8(operation.Type),
			)
		}
	}
	return nil
}

func applyOperations[T any](
	users map[adapter.UserID]T,
	operations []adapter.UserOperation[T],
) error {
	for index, operation := range operations {
		_, exists := users[operation.ID]
		switch operation.Type {
		case adapter.UserOperationAdd:
			if exists {
				return E.Cause(
					ErrUserExists,
					"operation ",
					index,
					" stable ID ",
					string(operation.ID),
				)
			}
			users[operation.ID] = operation.Value
		case adapter.UserOperationUpdate:
			if !exists {
				return E.Cause(
					ErrUserNotFound,
					"operation ",
					index,
					" stable ID ",
					string(operation.ID),
				)
			}
			users[operation.ID] = operation.Value
		case adapter.UserOperationDelete:
			if !exists {
				return E.Cause(
					ErrUserNotFound,
					"operation ",
					index,
					" stable ID ",
					string(operation.ID),
				)
			}
			delete(users, operation.ID)
		// This arm is defensive; validateOperations rejects unsupported types first.
		default:
			return E.Cause(
				ErrInvalidTransaction,
				"operation ",
				index,
				" has invalid type ",
				uint8(operation.Type),
			)
		}
	}
	return nil
}

func (m *Manager[T]) validateStableIDs(
	users map[adapter.UserID]T,
) ([]adapter.UserID, error) {
	sortedIDs := make([]adapter.UserID, 0, len(users))
	for id := range users {
		sortedIDs = append(sortedIDs, id)
	}
	slices.Sort(sortedIDs)
	if len(sortedIDs) == 0 {
		return sortedIDs, nil
	}

	seen := make(map[adapter.UserID]adapter.UserID, len(users))
	var mismatchedExpectedID adapter.UserID
	var mismatchedActualID adapter.UserID
	for _, expectedID := range sortedIDs {
		if expectedID == "" {
			return nil, ErrEmptyUserID
		}
		actualID, err := m.backend.StableID(users[expectedID])
		if err != nil {
			return nil, E.Cause1(
				ErrInvalidTransaction,
				E.Cause(err, "extract stable ID for user ", string(expectedID)),
			)
		}
		if actualID == "" {
			return nil, E.Cause(ErrEmptyUserID, "user ", string(expectedID))
		}
		if previousID, loaded := seen[actualID]; loaded {
			return nil, E.Cause(
				ErrDuplicateUserID,
				"users ",
				string(previousID),
				" and ",
				string(expectedID),
				" have stable ID ",
				string(actualID),
			)
		}
		seen[actualID] = expectedID
		if actualID != expectedID && mismatchedExpectedID == "" {
			mismatchedExpectedID = expectedID
			mismatchedActualID = actualID
		}
	}
	if mismatchedExpectedID != "" {
		return nil, E.Cause(
			ErrInvalidTransaction,
			"transaction stable ID ",
			string(mismatchedExpectedID),
			" does not match user stable ID ",
			string(mismatchedActualID),
		)
	}
	return sortedIDs, nil
}

func recordsByID[T any](
	users map[adapter.UserID]T,
	sortedIDs []adapter.UserID,
) []Record[T] {
	records := make([]Record[T], 0, len(sortedIDs))
	for _, id := range sortedIDs {
		records = append(records, Record[T]{
			ID:    id,
			Value: users[id],
		})
	}
	return records
}

func (m *Manager[T]) prepareReplacement(users []T) (*replacementState[T], error) {
	replacement := &replacementState[T]{
		users:      make(map[adapter.UserID]T, len(users)),
		orderedIDs: make([]adapter.UserID, 0, len(users)),
	}
	for index, user := range users {
		id, err := m.backend.StableID(user)
		if err != nil {
			return nil, E.Cause1(
				ErrInvalidTransaction,
				E.Cause(err, "extract replacement stable ID at index ", index),
			)
		}
		if id == "" {
			return nil, E.Cause(ErrEmptyUserID, "replacement index ", index)
		}
		if _, loaded := replacement.users[id]; loaded {
			return nil, E.Cause(
				ErrDuplicateUserID,
				"replacement index ",
				index,
				" repeats stable ID ",
				string(id),
			)
		}
		replacement.users[id] = user
		replacement.orderedIDs = append(replacement.orderedIDs, id)
	}
	return replacement, nil
}

func (m *Manager[T]) replacementOperations(
	replacement *replacementState[T],
) []adapter.UserOperation[T] {
	deletedIDs := make([]adapter.UserID, 0)
	for id := range m.canonical {
		if _, retained := replacement.users[id]; !retained {
			deletedIDs = append(deletedIDs, id)
		}
	}
	slices.Sort(deletedIDs)

	operations := make(
		[]adapter.UserOperation[T],
		0,
		len(replacement.orderedIDs)+len(deletedIDs),
	)
	for _, id := range replacement.orderedIDs {
		operationType := adapter.UserOperationUpdate
		if _, exists := m.canonical[id]; !exists {
			operationType = adapter.UserOperationAdd
		}
		operations = append(operations, adapter.UserOperation[T]{
			Type:  operationType,
			ID:    id,
			Value: replacement.users[id],
		})
	}
	for _, id := range deletedIDs {
		operations = append(operations, adapter.UserOperation[T]{
			Type: adapter.UserOperationDelete,
			ID:   id,
		})
	}
	return operations
}

func buildTransactionResult[T any](
	previousGeneration adapter.UserGeneration,
	generation adapter.UserGeneration,
	operations []adapter.UserOperation[T],
) adapter.UserTransactionResult {
	var addedCount int
	var updatedCount int
	var deletedCount int
	for _, operation := range operations {
		switch operation.Type {
		case adapter.UserOperationAdd:
			addedCount++
		case adapter.UserOperationUpdate:
			updatedCount++
		case adapter.UserOperationDelete:
			deletedCount++
		}
	}
	result := adapter.UserTransactionResult{
		PreviousGeneration: previousGeneration,
		Generation:         generation,
		Added:              make([]adapter.UserID, 0, addedCount),
		Updated:            make([]adapter.UserID, 0, updatedCount),
		Deleted:            make([]adapter.UserID, 0, deletedCount),
	}
	for _, operation := range operations {
		switch operation.Type {
		case adapter.UserOperationAdd:
			result.Added = append(result.Added, operation.ID)
		case adapter.UserOperationUpdate:
			result.Updated = append(result.Updated, operation.ID)
		case adapter.UserOperationDelete:
			result.Deleted = append(result.Deleted, operation.ID)
		}
	}
	slices.Sort(result.Added)
	slices.Sort(result.Updated)
	slices.Sort(result.Deleted)
	return result
}

func (m *Manager[T]) runPostCommitHooks(result adapter.UserTransactionResult) {
	for _, hook := range m.postCommitHooks {
		runPostCommitHook(hook, cloneTransactionResult(result))
	}
}

func runPostCommitHook(hook func(adapter.UserTransactionResult), result adapter.UserTransactionResult) {
	defer func() {
		_ = recover()
	}()
	hook(result)
}
