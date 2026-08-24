package usermanager

import E "github.com/sagernet/sing/common/exceptions"

var (
	// ErrInvalidTransaction indicates an unsupported or inconsistent transaction shape.
	ErrInvalidTransaction = E.New("invalid transaction")
	// ErrEmptyUserID indicates that a stable user ID is empty.
	ErrEmptyUserID = E.New("empty stable ID")
	// ErrDuplicateUserID indicates that a stable user ID appears more than once.
	ErrDuplicateUserID = E.New("duplicate stable ID")
	// ErrUserExists indicates that an add operation targets an existing user.
	ErrUserExists = E.New("user already exists")
	// ErrUserNotFound indicates that an update or delete operation targets a missing user.
	ErrUserNotFound = E.New("user not found")
	// ErrGenerationConflict indicates that the expected runtime generation is stale.
	ErrGenerationConflict = E.New("generation conflict")
	// ErrRequestIDConflict indicates that a request ID was reused with different content.
	ErrRequestIDConflict = E.New("request ID conflict")
	// ErrBackendPrepareFailure indicates that a backend could not prepare the final state.
	ErrBackendPrepareFailure = E.New("backend prepare failure")
)

func wrapBackendPrepareError(err error) error {
	return E.Cause1(ErrBackendPrepareFailure, E.Cause(err, "prepare"))
}
