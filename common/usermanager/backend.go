package usermanager

import "github.com/sagernet/sing-box/adapter"

// Record associates a stable ID with its typed canonical user value.
type Record[T any] struct {
	ID    adapter.UserID
	Value T
}

// Published is one complete immutable backend state prepared for publication.
type Published interface {
	// Commit atomically installs the prepared state. It must not fail or panic.
	Commit()
}

// Backend compiles a complete immutable user state and publishes it atomically.
type Backend[T any] interface {
	// StableID extracts and validates the stable identity of a typed user record.
	StableID(user T) (adapter.UserID, error)
	// Prepare validates the complete final user set and builds one immutable
	// generation. Records are sorted by ID. The manager does not deep-copy T;
	// a backend must deep-copy any reference-typed fields it retains. Prepare
	// must not mutate live state.
	Prepare(users []Record[T]) (Published, error)
}

// Fingerprinter is an optional Backend capability. Implementing it is strongly
// recommended. A backend that can cheaply summarise a user's credential-bearing
// fields should implement it, so that request-ID replay distinguishes two
// transactions differing only by value. Without it, the manager rejects a
// repeated request ID still in the replay cache when the current transaction
// carries values, because an identical replay cannot be proven. Delete-only
// transactions and empty replacements remain replay-eligible.
type Fingerprinter[T any] interface {
	FingerprintUser(user T) uint64
}
