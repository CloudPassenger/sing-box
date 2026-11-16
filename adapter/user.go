package adapter

import "context"

// UserID is the stable, non-positional identity of a managed user.
type UserID string

// UserGeneration is the runtime generation of one managed-user manager instance.
type UserGeneration uint64

// UserOperationType identifies a managed-user mutation.
type UserOperationType uint8

const (
	// UserOperationAdd adds a new stable user ID.
	UserOperationAdd UserOperationType = iota + 1
	// UserOperationUpdate replaces the value associated with an existing stable user ID.
	UserOperationUpdate
	// UserOperationDelete removes an existing stable user ID.
	UserOperationDelete
)

// UserOperation is one typed mutation in a user transaction.
type UserOperation[T any] struct {
	Type UserOperationType
	ID   UserID
	// Value is ignored for delete. The manager does not deep-copy T. After
	// submission, callers must not retain references to or mutate any
	// reference-typed fields reachable from Value.
	Value T
}

// UserTransaction is an atomic set of managed-user mutations.
type UserTransaction[T any] struct {
	// ExpectedGeneration is a local compare-and-swap guard. Zero means unconditional.
	ExpectedGeneration UserGeneration
	// RequestID optionally enables bounded local replay detection.
	RequestID string
	// SourceRevision is opaque caller diagnostic metadata. The kernel never interprets or persists it.
	SourceRevision string
	Operations     []UserOperation[T]
}

// UserTransactionResult describes one committed managed-user generation.
type UserTransactionResult struct {
	PreviousGeneration UserGeneration
	Generation         UserGeneration
	Added              []UserID
	Updated            []UserID
	Deleted            []UserID
	Replayed           bool
}

// ManagedUserManager applies atomic typed user transactions to one inbound.
// Transactions affect future authentication only. Deleting a user does not revoke
// live sessions; session revocation is a separate future capability.
type ManagedUserManager[T any] interface {
	Generation() UserGeneration
	// ApplyUsers atomically applies a typed delta to future authentication state.
	// Context cancellation is not honored because the non-blocking commit path
	// must not be interrupted mid-transaction.
	ApplyUsers(ctx context.Context, transaction UserTransaction[T]) (UserTransactionResult, error)
	// ReplaceUsers atomically reconciles the complete user set for future authentication.
	// It does not revoke live sessions removed by the replacement. Every retained
	// ID is reported as updated even when its value is unchanged, and every
	// successful replacement advances the generation. Context cancellation is not
	// honored because the non-blocking commit path must not be interrupted.
	ReplaceUsers(
		ctx context.Context,
		expectedGeneration UserGeneration,
		requestID string,
		sourceRevision string,
		users []T,
	) (UserTransactionResult, error)
}
