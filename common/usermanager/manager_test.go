package usermanager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"github.com/stretchr/testify/require"
)

const (
	testFingerprintOffset64 uint64 = 14695981039346656037
	testFingerprintPrime64  uint64 = 1099511628211
)

type testUser struct {
	ID         adapter.UserID
	Credential string
}

type fakePrepareError struct {
	message string
}

func (e *fakePrepareError) Error() string {
	return e.message
}

type fakeBackendSnapshot struct {
	PrepareCalls int
	CommitCalls  int
	Prepared     [][]Record[testUser]
	Published    []Record[testUser]
}

type fakeBackend struct {
	mutex              sync.Mutex
	stableIDCalls      atomic.Int32
	fingerprintCalls   atomic.Int32
	prepareCalls       int
	commitCalls        int
	prepared           [][]Record[testUser]
	published          []Record[testUser]
	stableIDErr        error
	prepareErr         error
	prepareNil         bool
	prepareStarted     chan struct{}
	prepareRelease     <-chan struct{}
	prepareStartedOnce sync.Once
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		prepared:  make([][]Record[testUser], 0),
		published: make([]Record[testUser], 0),
	}
}

func (b *fakeBackend) StableID(user testUser) (adapter.UserID, error) {
	b.stableIDCalls.Add(1)
	b.mutex.Lock()
	stableIDErr := b.stableIDErr
	b.mutex.Unlock()
	if stableIDErr != nil {
		return "", stableIDErr
	}
	return user.ID, nil
}

func (b *fakeBackend) FingerprintUser(user testUser) uint64 {
	b.fingerprintCalls.Add(1)
	fingerprint := testFingerprintOffset64
	for index := range len(user.Credential) {
		fingerprint = (fingerprint ^ uint64(user.Credential[index])) * testFingerprintPrime64
	}
	return fingerprint
}

func (b *fakeBackend) Prepare(users []Record[testUser]) (Published, error) {
	records := cloneTestRecords(users)

	b.mutex.Lock()
	b.prepareCalls++
	b.prepared = append(b.prepared, cloneTestRecords(records))
	prepareErr := b.prepareErr
	prepareNil := b.prepareNil
	prepareStarted := b.prepareStarted
	prepareRelease := b.prepareRelease
	b.mutex.Unlock()

	if prepareStarted != nil {
		b.prepareStartedOnce.Do(func() {
			close(prepareStarted)
		})
	}
	if prepareRelease != nil {
		<-prepareRelease
	}
	if prepareErr != nil {
		return nil, prepareErr
	}
	if prepareNil {
		return nil, nil
	}

	credentials := make(map[string]adapter.UserID, len(records))
	for _, record := range records {
		if previousID, loaded := credentials[record.Value.Credential]; loaded {
			return nil, fmt.Errorf(
				"duplicate credential %q for users %q and %q",
				record.Value.Credential,
				previousID,
				record.ID,
			)
		}
		credentials[record.Value.Credential] = record.ID
	}

	return &fakePublished{
		backend: b,
		records: records,
	}, nil
}

func (b *fakeBackend) setPrepareError(err error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.prepareErr = err
}

func (b *fakeBackend) setStableIDError(err error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.stableIDErr = err
}

func (b *fakeBackend) setPrepareNil(prepareNil bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.prepareNil = prepareNil
}

func (b *fakeBackend) blockNextPrepare(started chan struct{}, release <-chan struct{}) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.prepareStarted = started
	b.prepareRelease = release
}

func (b *fakeBackend) snapshot() fakeBackendSnapshot {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	prepared := make([][]Record[testUser], len(b.prepared))
	for index, records := range b.prepared {
		prepared[index] = cloneTestRecords(records)
	}
	return fakeBackendSnapshot{
		PrepareCalls: b.prepareCalls,
		CommitCalls:  b.commitCalls,
		Prepared:     prepared,
		Published:    cloneTestRecords(b.published),
	}
}

type fakePublished struct {
	backend *fakeBackend
	records []Record[testUser]
}

func (p *fakePublished) Commit() {
	p.backend.mutex.Lock()
	defer p.backend.mutex.Unlock()
	p.backend.commitCalls++
	p.backend.published = cloneTestRecords(p.records)
}

type fakeBackendWithoutFingerprint struct {
	backend *fakeBackend
}

func (b *fakeBackendWithoutFingerprint) StableID(user testUser) (adapter.UserID, error) {
	return b.backend.StableID(user)
}

func (b *fakeBackendWithoutFingerprint) Prepare(users []Record[testUser]) (Published, error) {
	return b.backend.Prepare(users)
}

type applyOutcome struct {
	result adapter.UserTransactionResult
	err    error
}

var (
	_ Backend[testUser]                    = (*fakeBackend)(nil)
	_ Fingerprinter[testUser]              = (*fakeBackend)(nil)
	_ Backend[testUser]                    = (*fakeBackendWithoutFingerprint)(nil)
	_ adapter.ManagedUserManager[testUser] = (*Manager[testUser])(nil)
)

func cloneTestRecords(records []Record[testUser]) []Record[testUser] {
	return slices.Clone(records)
}

func sortTestRecords(records []Record[testUser]) {
	slices.SortFunc(records, func(left Record[testUser], right Record[testUser]) int {
		switch {
		case left.ID < right.ID:
			return -1
		case left.ID > right.ID:
			return 1
		default:
			return 0
		}
	})
}

func testRecords(users ...testUser) []Record[testUser] {
	records := make([]Record[testUser], 0, len(users))
	for _, user := range users {
		records = append(records, Record[testUser]{
			ID:    user.ID,
			Value: user,
		})
	}
	sortTestRecords(records)
	return records
}

func canonicalTestRecords(manager *Manager[testUser]) []Record[testUser] {
	manager.updateMutex.Lock()
	defer manager.updateMutex.Unlock()

	records := make([]Record[testUser], 0, len(manager.canonical))
	for id, user := range manager.canonical {
		records = append(records, Record[testUser]{
			ID:    id,
			Value: user,
		})
	}
	sortTestRecords(records)
	return records
}

func seedTestUsers(
	t *testing.T,
	manager *Manager[testUser],
	users ...testUser,
) adapter.UserTransactionResult {
	t.Helper()
	operations := make([]adapter.UserOperation[testUser], 0, len(users))
	for _, user := range users {
		operations = append(operations, adapter.UserOperation[testUser]{
			Type:  adapter.UserOperationAdd,
			ID:    user.ID,
			Value: user,
		})
	}
	result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: operations,
	})
	require.NoError(t, err)
	return result
}

func requireManagerState(
	t *testing.T,
	manager *Manager[testUser],
	backend *fakeBackend,
	expected []Record[testUser],
) {
	t.Helper()
	require.Equal(t, expected, canonicalTestRecords(manager))
	require.Equal(t, expected, backend.snapshot().Published)
}

func TestManagedUserManagerContract(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	var managed adapter.ManagedUserManager[testUser] = manager

	require.Equal(t, adapter.UserGeneration(0), managed.Generation())
	require.Empty(t, canonicalTestRecords(manager))
	require.Empty(t, backend.snapshot().Published)
}

func TestManagerApplyUsersMixedTransaction(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager,
		testUser{ID: "a", Credential: "password-a"},
		testUser{ID: "b", Credential: "password-b"},
	)
	before := backend.snapshot()

	result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		ExpectedGeneration: 1,
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "c",
				Value: testUser{ID: "c", Credential: "password-c"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "password-a-rotated"},
			},
			{
				Type: adapter.UserOperationDelete,
				ID:   "b",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserTransactionResult{
		PreviousGeneration: 1,
		Generation:         2,
		Added:              []adapter.UserID{"c"},
		Updated:            []adapter.UserID{"a"},
		Deleted:            []adapter.UserID{"b"},
	}, result)
	require.Equal(t, adapter.UserGeneration(2), manager.Generation())

	expected := testRecords(
		testUser{ID: "a", Credential: "password-a-rotated"},
		testUser{ID: "c", Credential: "password-c"},
	)
	after := backend.snapshot()
	require.Equal(t, before.PrepareCalls+1, after.PrepareCalls)
	require.Equal(t, before.CommitCalls+1, after.CommitCalls)
	require.Equal(t, expected, after.Prepared[len(after.Prepared)-1])
	requireManagerState(t, manager, backend, expected)
}

func TestManagerRejectsMalformedOperationsBeforePrepare(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		operations []adapter.UserOperation[testUser]
		target     error
	}{
		{
			name: "invalid type",
			operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationType(255),
					ID:    "a",
					Value: testUser{ID: "a", Credential: "invalid"},
				},
			},
			target: ErrInvalidTransaction,
		},
		{
			name: "empty operation ID",
			operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationUpdate,
					Value: testUser{ID: "a", Credential: "invalid"},
				},
			},
			target: ErrEmptyUserID,
		},
		{
			name: "duplicate operation ID",
			operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationUpdate,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "first"},
				},
				{
					Type:  adapter.UserOperationUpdate,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "second"},
				},
			},
			target: ErrDuplicateUserID,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := newFakeBackend()
			manager := New[testUser](backend, Options{})
			seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
			beforeBackend := backend.snapshot()
			beforeCanonical := canonicalTestRecords(manager)
			beforeGeneration := manager.Generation()

			_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
				Operations: test.operations,
			})
			require.ErrorIs(t, err, test.target)
			require.Equal(t, beforeGeneration, manager.Generation())
			require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
			require.Equal(t, beforeBackend, backend.snapshot())
		})
	}
}

func TestManagerApplyUsersIsAtomicOnSemanticFailure(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager,
		testUser{ID: "a", Credential: "password-a"},
		testUser{ID: "b", Credential: "password-b"},
	)
	beforeBackend := backend.snapshot()
	beforeCanonical := canonicalTestRecords(manager)
	beforeGeneration := manager.Generation()

	_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "password-a-rotated"},
			},
			{
				Type: adapter.UserOperationDelete,
				ID:   "missing",
			},
		},
	})
	require.ErrorIs(t, err, ErrUserNotFound)
	require.Equal(t, beforeGeneration, manager.Generation())
	require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
	require.Equal(t, beforeBackend, backend.snapshot())
}

func TestManagerAllowsCredentialSwap(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager,
		testUser{ID: "a", Credential: "credential-a"},
		testUser{ID: "b", Credential: "credential-b"},
	)

	result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		ExpectedGeneration: 1,
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "credential-b"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "b",
				Value: testUser{ID: "b", Credential: "credential-a"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"a", "b"}, result.Updated)
	require.Equal(t, adapter.UserGeneration(2), result.Generation)
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "a", Credential: "credential-b"},
		testUser{ID: "b", Credential: "credential-a"},
	))
}

func TestManagerRejectsInvalidStableIDsBeforePrepare(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target error
		run    func(*Manager[testUser]) error
	}{
		{
			name:   "ApplyUsers duplicate stable ID",
			target: ErrDuplicateUserID,
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					Operations: []adapter.UserOperation[testUser]{
						{
							Type:  adapter.UserOperationAdd,
							ID:    "left",
							Value: testUser{ID: "shared", Credential: "left-credential"},
						},
						{
							Type:  adapter.UserOperationAdd,
							ID:    "right",
							Value: testUser{ID: "shared", Credential: "right-credential"},
						},
					},
				})
				return err
			},
		},
		{
			name:   "ApplyUsers empty stable ID",
			target: ErrEmptyUserID,
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					Operations: []adapter.UserOperation[testUser]{
						{
							Type:  adapter.UserOperationAdd,
							ID:    "a",
							Value: testUser{Credential: "credential-a"},
						},
					},
				})
				return err
			},
		},
		{
			name:   "ApplyUsers transaction ID mismatch",
			target: ErrInvalidTransaction,
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					Operations: []adapter.UserOperation[testUser]{
						{
							Type:  adapter.UserOperationAdd,
							ID:    "expected",
							Value: testUser{ID: "actual", Credential: "credential-a"},
						},
					},
				})
				return err
			},
		},
		{
			name:   "validateStableIDs empty map key",
			target: ErrEmptyUserID,
			run: func(manager *Manager[testUser]) error {
				_, err := manager.validateStableIDs(map[adapter.UserID]testUser{
					"": {ID: "a", Credential: "credential-a"},
				})
				return err
			},
		},
		{
			name:   "ReplaceUsers duplicate stable ID",
			target: ErrDuplicateUserID,
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ReplaceUsers(
					context.Background(),
					0,
					"",
					"",
					[]testUser{
						{ID: "shared", Credential: "left-credential"},
						{ID: "shared", Credential: "right-credential"},
					},
				)
				return err
			},
		},
		{
			name:   "ReplaceUsers empty stable ID",
			target: ErrEmptyUserID,
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ReplaceUsers(
					context.Background(),
					0,
					"",
					"",
					[]testUser{{Credential: "credential-a"}},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := newFakeBackend()
			manager := New[testUser](backend, Options{})

			err := test.run(manager)
			require.ErrorIs(t, err, test.target)
			require.Equal(t, adapter.UserGeneration(0), manager.Generation())
			require.Empty(t, canonicalTestRecords(manager))
			snapshot := backend.snapshot()
			require.Zero(t, snapshot.PrepareCalls)
			require.Zero(t, snapshot.CommitCalls)
			require.Empty(t, snapshot.Published)
		})
	}
}

func TestManagerRejectsStaleExpectedGenerationBeforePrepare(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
	beforeBackend := backend.snapshot()
	beforeCanonical := canonicalTestRecords(manager)

	_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		ExpectedGeneration: 2,
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "password-a-rotated"},
			},
		},
	})
	require.ErrorIs(t, err, ErrGenerationConflict)
	require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
	require.Equal(t, beforeBackend, backend.snapshot())
}

func TestManagerAcceptsUnconditionalExpectedGeneration(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})

	result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		ExpectedGeneration: 0,
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "password-a-rotated"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(1), result.PreviousGeneration)
	require.Equal(t, adapter.UserGeneration(2), result.Generation)
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "a", Credential: "password-a-rotated"},
	))
}

//nolint:paralleltest // timing-sensitive; must not contend with parallel tests
func TestManagerConcurrentWritersSerializeWithoutLostUpdates(t *testing.T) {
	const writerCount = 16

	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	start := make(chan struct{})
	results := make([]adapter.UserTransactionResult, writerCount)
	errs := make([]error, writerCount)
	users := make([]testUser, writerCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(writerCount)

	for index := range writerCount {
		users[index] = testUser{
			ID:         adapter.UserID(fmt.Sprintf("user-%02d", index)),
			Credential: fmt.Sprintf("credential-%02d", index),
		}
		go func(index int) {
			defer waitGroup.Done()
			<-start
			results[index], errs[index] = manager.ApplyUsers(
				context.Background(),
				adapter.UserTransaction[testUser]{
					Operations: []adapter.UserOperation[testUser]{
						{
							Type:  adapter.UserOperationAdd,
							ID:    users[index].ID,
							Value: users[index],
						},
					},
				},
			)
		}(index)
	}

	close(start)
	waitGroup.Wait()

	generations := make([]adapter.UserGeneration, 0, writerCount)
	for index, err := range errs {
		require.NoErrorf(t, err, "writer %d", index)
		require.Equal(t, results[index].PreviousGeneration+1, results[index].Generation)
		generations = append(generations, results[index].Generation)
	}
	slices.Sort(generations)
	for index, generation := range generations {
		require.Equal(t, adapter.UserGeneration(index+1), generation)
	}

	require.Equal(t, adapter.UserGeneration(writerCount), manager.Generation())
	snapshot := backend.snapshot()
	require.Equal(t, writerCount, snapshot.PrepareCalls)
	require.Equal(t, writerCount, snapshot.CommitCalls)
	requireManagerState(t, manager, backend, testRecords(users...))
}

func TestManagerRequestIDReplayReturnsOriginalResultAndRunsHooksOnce(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	var hookCalls atomic.Int32
	manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
		hookCalls.Add(1)
	})
	transaction := adapter.UserTransaction[testUser]{
		RequestID:      "request-1",
		SourceRevision: "revision-1",
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "password-a"},
			},
		},
	}

	first, err := manager.ApplyUsers(context.Background(), transaction)
	require.NoError(t, err)
	second, err := manager.ApplyUsers(context.Background(), transaction)
	require.NoError(t, err)

	expectedReplay := first
	expectedReplay.Replayed = true
	require.Equal(t, expectedReplay, second)
	require.False(t, first.Replayed)
	require.Equal(t, int32(1), hookCalls.Load())
	require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	snapshot := backend.snapshot()
	require.Equal(t, 1, snapshot.PrepareCalls)
	require.Equal(t, 1, snapshot.CommitCalls)
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "a", Credential: "password-a"},
	))
}

func TestManagerRejectsRequestIDReuseWithDifferentContent(t *testing.T) {
	t.Parallel()

	t.Run("ApplyUsers credential rotation", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](backend, Options{})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-initial"})

		firstTransaction := adapter.UserTransaction[testUser]{
			RequestID: "rotation-request",
			Operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationUpdate,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "ROTATED-1"},
				},
			},
		}
		first, err := manager.ApplyUsers(context.Background(), firstTransaction)
		require.NoError(t, err)
		require.Equal(t, adapter.UserGeneration(2), first.Generation)
		beforeBackend := backend.snapshot()
		beforeCanonical := canonicalTestRecords(manager)

		secondTransaction := firstTransaction
		secondTransaction.Operations = []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "ROTATED-2"},
			},
		}
		_, err = manager.ApplyUsers(context.Background(), secondTransaction)
		require.ErrorIs(t, err, ErrRequestIDConflict)
		require.Equal(t, adapter.UserGeneration(2), manager.Generation())
		require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
		require.Equal(t, beforeBackend, backend.snapshot())
		requireManagerState(t, manager, backend, testRecords(
			testUser{ID: "a", Credential: "ROTATED-1"},
		))
	})

	t.Run("ReplaceUsers credential rotation", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](backend, Options{})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-initial"})

		first, err := manager.ReplaceUsers(
			context.Background(),
			0,
			"replacement-request",
			"revision-1",
			[]testUser{{ID: "a", Credential: "ROTATED-1"}},
		)
		require.NoError(t, err)
		require.Equal(t, adapter.UserGeneration(2), first.Generation)
		beforeBackend := backend.snapshot()
		beforeCanonical := canonicalTestRecords(manager)

		_, err = manager.ReplaceUsers(
			context.Background(),
			0,
			"replacement-request",
			"revision-2",
			[]testUser{{ID: "a", Credential: "ROTATED-2"}},
		)
		require.ErrorIs(t, err, ErrRequestIDConflict)
		require.Equal(t, adapter.UserGeneration(2), manager.Generation())
		require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
		require.Equal(t, beforeBackend, backend.snapshot())
		requireManagerState(t, manager, backend, testRecords(
			testUser{ID: "a", Credential: "ROTATED-1"},
		))
	})
}

func TestManagerReplaceUsersMatchesEquivalentDelta(t *testing.T) {
	t.Parallel()
	deltaBackend := newFakeBackend()
	deltaManager := New[testUser](deltaBackend, Options{})
	replaceBackend := newFakeBackend()
	replaceManager := New[testUser](replaceBackend, Options{})
	initial := []testUser{
		{ID: "a", Credential: "credential-a"},
		{ID: "b", Credential: "credential-b"},
		{ID: "d", Credential: "credential-d"},
	}
	seedTestUsers(t, deltaManager, initial...)
	seedTestUsers(t, replaceManager, initial...)

	deltaResult, err := deltaManager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		ExpectedGeneration: 1,
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "c",
				Value: testUser{ID: "c", Credential: "credential-c"},
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "d",
				Value: testUser{ID: "d", Credential: "credential-d"},
			},
			{
				Type: adapter.UserOperationDelete,
				ID:   "b",
			},
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "credential-a-rotated"},
			},
		},
	})
	require.NoError(t, err)

	replaceResult, err := replaceManager.ReplaceUsers(
		context.Background(),
		1,
		"",
		"revision-2",
		[]testUser{
			{ID: "d", Credential: "credential-d"},
			{ID: "c", Credential: "credential-c"},
			{ID: "a", Credential: "credential-a-rotated"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, deltaResult, replaceResult)
	require.Equal(t, canonicalTestRecords(deltaManager), canonicalTestRecords(replaceManager))
	require.Equal(t, deltaBackend.snapshot().Published, replaceBackend.snapshot().Published)
	require.Equal(t, adapter.UserGeneration(2), deltaManager.Generation())
	require.Equal(t, adapter.UserGeneration(2), replaceManager.Generation())
}

func TestManagerReplaceUsersAcceptsDeleteAll(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager,
		testUser{ID: "c", Credential: "credential-c"},
		testUser{ID: "a", Credential: "credential-a"},
		testUser{ID: "b", Credential: "credential-b"},
	)

	result, err := manager.ReplaceUsers(
		context.Background(),
		1,
		"delete-all",
		"revision-empty",
		[]testUser{},
	)
	require.NoError(t, err)
	require.Equal(t, adapter.UserTransactionResult{
		PreviousGeneration: 1,
		Generation:         2,
		Added:              []adapter.UserID{},
		Updated:            []adapter.UserID{},
		Deleted:            []adapter.UserID{"a", "b", "c"},
	}, result)
	require.Empty(t, canonicalTestRecords(manager))
	snapshot := backend.snapshot()
	require.Equal(t, 2, snapshot.PrepareCalls)
	require.Equal(t, 2, snapshot.CommitCalls)
	require.Empty(t, snapshot.Prepared[len(snapshot.Prepared)-1])
	require.Empty(t, snapshot.Published)
}

func TestManagerResultOrderingIsDeterministic(t *testing.T) {
	t.Parallel()
	const iterations = 32
	expectedResult := adapter.UserTransactionResult{
		PreviousGeneration: 1,
		Generation:         2,
		Added:              []adapter.UserID{"a", "c", "e"},
		Updated:            []adapter.UserID{"b", "f"},
		Deleted:            []adapter.UserID{"d"},
	}
	expectedRecords := testRecords(
		testUser{ID: "a", Credential: "credential-a"},
		testUser{ID: "b", Credential: "credential-b-rotated"},
		testUser{ID: "c", Credential: "credential-c"},
		testUser{ID: "e", Credential: "credential-e"},
		testUser{ID: "f", Credential: "credential-f-rotated"},
	)
	initialOrders := [][]testUser{
		{
			{ID: "b", Credential: "credential-b"},
			{ID: "d", Credential: "credential-d"},
			{ID: "f", Credential: "credential-f"},
		},
		{
			{ID: "f", Credential: "credential-f"},
			{ID: "b", Credential: "credential-b"},
			{ID: "d", Credential: "credential-d"},
		},
		{
			{ID: "d", Credential: "credential-d"},
			{ID: "f", Credential: "credential-f"},
			{ID: "b", Credential: "credential-b"},
		},
	}
	baseOperations := []adapter.UserOperation[testUser]{
		{
			Type: adapter.UserOperationDelete,
			ID:   "d",
		},
		{
			Type:  adapter.UserOperationAdd,
			ID:    "e",
			Value: testUser{ID: "e", Credential: "credential-e"},
		},
		{
			Type:  adapter.UserOperationUpdate,
			ID:    "f",
			Value: testUser{ID: "f", Credential: "credential-f-rotated"},
		},
		{
			Type:  adapter.UserOperationAdd,
			ID:    "a",
			Value: testUser{ID: "a", Credential: "credential-a"},
		},
		{
			Type:  adapter.UserOperationUpdate,
			ID:    "b",
			Value: testUser{ID: "b", Credential: "credential-b-rotated"},
		},
		{
			Type:  adapter.UserOperationAdd,
			ID:    "c",
			Value: testUser{ID: "c", Credential: "credential-c"},
		},
	}

	for iteration := range iterations {
		backend := newFakeBackend()
		manager := New[testUser](backend, Options{})
		seedTestUsers(t, manager, initialOrders[iteration%len(initialOrders)]...)
		shift := iteration % len(baseOperations)
		operations := append(
			slices.Clone(baseOperations[shift:]),
			baseOperations[:shift]...,
		)

		result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
			Operations: operations,
		})
		require.NoErrorf(t, err, "iteration %d", iteration)
		require.Equalf(t, expectedResult, result, "iteration %d", iteration)
		snapshot := backend.snapshot()
		require.Equalf(
			t,
			expectedRecords,
			snapshot.Prepared[len(snapshot.Prepared)-1],
			"iteration %d",
			iteration,
		)
		requireManagerState(t, manager, backend, expectedRecords)
	}
}

func TestManagerBackendPrepareFailureRollsBackAllStateAndPreservesCause(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
	backendErr := &fakePrepareError{message: "credential compiler failed"}
	backend.setPrepareError(backendErr)
	beforeBackend := backend.snapshot()
	beforeCanonical := canonicalTestRecords(manager)
	beforeGeneration := manager.Generation()

	_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationUpdate,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "password-a-rotated"},
			},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrBackendPrepareFailure))
	require.True(t, errors.Is(err, backendErr))
	var unwrapped *fakePrepareError
	require.True(t, errors.As(err, &unwrapped))
	require.Same(t, backendErr, unwrapped)
	require.Equal(t, beforeGeneration, manager.Generation())
	require.Equal(t, beforeCanonical, canonicalTestRecords(manager))

	afterBackend := backend.snapshot()
	require.Equal(t, beforeBackend.PrepareCalls+1, afterBackend.PrepareCalls)
	require.Equal(t, beforeBackend.CommitCalls, afterBackend.CommitCalls)
	require.Equal(t, beforeBackend.Published, afterBackend.Published)
	require.Equal(t, testRecords(
		testUser{ID: "a", Credential: "password-a-rotated"},
	), afterBackend.Prepared[len(afterBackend.Prepared)-1])
}

func TestManagerReplayCacheOptions(t *testing.T) {
	t.Parallel()

	t.Run("negative capacity disables replay", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](backend, Options{ReplayCacheSize: -1})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
		var hookCalls atomic.Int32
		manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
			hookCalls.Add(1)
		})
		transaction := adapter.UserTransaction[testUser]{
			RequestID: "repeat-with-cache-disabled",
			Operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationUpdate,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "password-a-rotated"},
				},
			},
		}

		first, err := manager.ApplyUsers(context.Background(), transaction)
		require.NoError(t, err)
		second, err := manager.ApplyUsers(context.Background(), transaction)
		require.NoError(t, err)
		require.False(t, first.Replayed)
		require.False(t, second.Replayed)
		require.Equal(t, adapter.UserGeneration(2), first.Generation)
		require.Equal(t, adapter.UserGeneration(3), second.Generation)
		require.Equal(t, int32(2), hookCalls.Load())
		snapshot := backend.snapshot()
		require.Equal(t, 3, snapshot.PrepareCalls)
		require.Equal(t, 3, snapshot.CommitCalls)
	})

	t.Run("positive capacity evicts FIFO", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](backend, Options{ReplayCacheSize: 2})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-initial"})
		transactions := []adapter.UserTransaction[testUser]{
			{
				RequestID: "request-1",
				Operations: []adapter.UserOperation[testUser]{
					{
						Type:  adapter.UserOperationUpdate,
						ID:    "a",
						Value: testUser{ID: "a", Credential: "password-1"},
					},
				},
			},
			{
				RequestID: "request-2",
				Operations: []adapter.UserOperation[testUser]{
					{
						Type:  adapter.UserOperationUpdate,
						ID:    "a",
						Value: testUser{ID: "a", Credential: "password-2"},
					},
				},
			},
			{
				RequestID: "request-3",
				Operations: []adapter.UserOperation[testUser]{
					{
						Type:  adapter.UserOperationUpdate,
						ID:    "a",
						Value: testUser{ID: "a", Credential: "password-3"},
					},
				},
			},
		}
		for _, transaction := range transactions {
			result, err := manager.ApplyUsers(context.Background(), transaction)
			require.NoError(t, err)
			require.False(t, result.Replayed)
		}
		require.Equal(t, adapter.UserGeneration(4), manager.Generation())

		reexecuted, err := manager.ApplyUsers(context.Background(), transactions[0])
		require.NoError(t, err)
		require.False(t, reexecuted.Replayed)
		require.Equal(t, adapter.UserGeneration(4), reexecuted.PreviousGeneration)
		require.Equal(t, adapter.UserGeneration(5), reexecuted.Generation)
		require.Equal(t, adapter.UserGeneration(5), manager.Generation())
		snapshot := backend.snapshot()
		require.Equal(t, 5, snapshot.PrepareCalls)
		require.Equal(t, 5, snapshot.CommitCalls)
		requireManagerState(t, manager, backend, testRecords(
			testUser{ID: "a", Credential: "password-1"},
		))
	})
}

//nolint:paralleltest // timing-sensitive; must not contend with parallel tests
func TestManagerGenerationDoesNotBlockDuringPrepare(t *testing.T) {
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	prepareStarted := make(chan struct{})
	prepareRelease := make(chan struct{})
	backend.blockNextPrepare(prepareStarted, prepareRelease)
	var releaseOnce sync.Once
	releasePrepare := func() {
		releaseOnce.Do(func() {
			close(prepareRelease)
		})
	}
	t.Cleanup(releasePrepare)

	outcomeChannel := make(chan applyOutcome, 1)
	go func() {
		result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
			Operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationAdd,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "password-a"},
				},
			},
		})
		outcomeChannel <- applyOutcome{result: result, err: err}
	}()

	select {
	case <-prepareStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("transaction did not reach backend Prepare")
	}

	generationChannel := make(chan adapter.UserGeneration, 1)
	go func() {
		generationChannel <- manager.Generation()
	}()
	select {
	case generation := <-generationChannel:
		require.Equal(t, adapter.UserGeneration(0), generation)
	case <-time.After(2 * time.Second):
		t.Fatal("Generation blocked behind in-flight Prepare")
	}

	releasePrepare()
	select {
	case outcome := <-outcomeChannel:
		require.NoError(t, outcome.err)
		require.Equal(t, adapter.UserGeneration(1), outcome.result.Generation)
	case <-time.After(2 * time.Second):
		t.Fatal("transaction did not complete after Prepare was released")
	}
	require.Equal(t, adapter.UserGeneration(1), manager.Generation())
}

func TestManagerReplayWithoutFingerprinterFailsClosedForValueTransactions(t *testing.T) {
	t.Parallel()

	t.Run("ApplyUsers add", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](&fakeBackendWithoutFingerprint{backend: backend}, Options{})
		var hookCalls atomic.Int32
		manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
			hookCalls.Add(1)
		})
		transaction := adapter.UserTransaction[testUser]{
			RequestID: "add-request",
			Operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationAdd,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "password-a"},
				},
			},
		}

		_, err := manager.ApplyUsers(context.Background(), transaction)
		require.NoError(t, err)
		before := backend.snapshot()
		_, err = manager.ApplyUsers(context.Background(), transaction)
		require.ErrorIs(t, err, ErrRequestIDConflict)
		require.Equal(t, before, backend.snapshot())
		require.Equal(t, int32(1), hookCalls.Load())
		require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	})

	t.Run("ApplyUsers update", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](&fakeBackendWithoutFingerprint{backend: backend}, Options{})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-initial"})
		transaction := adapter.UserTransaction[testUser]{
			RequestID: "update-request",
			Operations: []adapter.UserOperation[testUser]{
				{
					Type:  adapter.UserOperationUpdate,
					ID:    "a",
					Value: testUser{ID: "a", Credential: "password-rotated"},
				},
			},
		}

		_, err := manager.ApplyUsers(context.Background(), transaction)
		require.NoError(t, err)
		before := backend.snapshot()
		_, err = manager.ApplyUsers(context.Background(), transaction)
		require.ErrorIs(t, err, ErrRequestIDConflict)
		require.Equal(t, before, backend.snapshot())
		require.Equal(t, adapter.UserGeneration(2), manager.Generation())
		requireManagerState(t, manager, backend, testRecords(
			testUser{ID: "a", Credential: "password-rotated"},
		))
	})

	t.Run("ReplaceUsers non-empty", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](&fakeBackendWithoutFingerprint{backend: backend}, Options{})
		users := []testUser{{ID: "a", Credential: "password-a"}}

		_, err := manager.ReplaceUsers(
			context.Background(),
			0,
			"replacement-request",
			"revision-1",
			users,
		)
		require.NoError(t, err)
		before := backend.snapshot()
		_, err = manager.ReplaceUsers(
			context.Background(),
			0,
			"replacement-request",
			"revision-1",
			users,
		)
		require.ErrorIs(t, err, ErrRequestIDConflict)
		require.Equal(t, before, backend.snapshot())
		require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	})
}

func TestManagerReplayWithoutFingerprinterAllowsValuelessTransactions(t *testing.T) {
	t.Parallel()

	t.Run("delete-only ApplyUsers", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](&fakeBackendWithoutFingerprint{backend: backend}, Options{})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
		var hookCalls atomic.Int32
		manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
			hookCalls.Add(1)
		})
		transaction := adapter.UserTransaction[testUser]{
			ExpectedGeneration: 0,
			RequestID:          "delete-request",
			Operations: []adapter.UserOperation[testUser]{
				{
					Type: adapter.UserOperationDelete,
					ID:   "a",
				},
			},
		}

		first, err := manager.ApplyUsers(context.Background(), transaction)
		require.NoError(t, err)
		second, err := manager.ApplyUsers(context.Background(), transaction)
		require.NoError(t, err)
		expectedReplay := first
		expectedReplay.Replayed = true
		require.Equal(t, expectedReplay, second)
		require.Equal(t, int32(1), hookCalls.Load())
		require.Equal(t, adapter.UserGeneration(2), manager.Generation())
		snapshot := backend.snapshot()
		require.Equal(t, 2, snapshot.PrepareCalls)
		require.Equal(t, 2, snapshot.CommitCalls)
		require.Empty(t, snapshot.Published)
	})

	t.Run("empty ReplaceUsers", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		manager := New[testUser](&fakeBackendWithoutFingerprint{backend: backend}, Options{})
		seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
		var hookCalls atomic.Int32
		manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
			hookCalls.Add(1)
		})

		first, err := manager.ReplaceUsers(
			context.Background(),
			0,
			"empty-replacement-request",
			"revision-empty",
			[]testUser{},
		)
		require.NoError(t, err)
		second, err := manager.ReplaceUsers(
			context.Background(),
			0,
			"empty-replacement-request",
			"revision-empty",
			[]testUser{},
		)
		require.NoError(t, err)
		expectedReplay := first
		expectedReplay.Replayed = true
		require.Equal(t, expectedReplay, second)
		require.Equal(t, int32(1), hookCalls.Load())
		require.Equal(t, adapter.UserGeneration(2), manager.Generation())
		snapshot := backend.snapshot()
		require.Equal(t, 2, snapshot.PrepareCalls)
		require.Equal(t, 2, snapshot.CommitCalls)
		require.Empty(t, snapshot.Published)
	})
}

func TestNewPanicsOnNilBackend(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "usermanager: nil backend", func() {
		New[testUser](nil, Options{})
	})
}

func TestManagerRejectsSemanticOperationErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation adapter.UserOperation[testUser]
		target    error
	}{
		{
			name: "add existing ID",
			operation: adapter.UserOperation[testUser]{
				Type:  adapter.UserOperationAdd,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "replacement"},
			},
			target: ErrUserExists,
		},
		{
			name: "update missing ID",
			operation: adapter.UserOperation[testUser]{
				Type:  adapter.UserOperationUpdate,
				ID:    "missing",
				Value: testUser{ID: "missing", Credential: "replacement"},
			},
			target: ErrUserNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := newFakeBackend()
			manager := New[testUser](backend, Options{})
			seedTestUsers(t, manager, testUser{ID: "a", Credential: "password-a"})
			beforeBackend := backend.snapshot()
			beforeCanonical := canonicalTestRecords(manager)
			beforeGeneration := manager.Generation()

			_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
				Operations: []adapter.UserOperation[testUser]{test.operation},
			})
			require.ErrorIs(t, err, test.target)
			require.Equal(t, beforeGeneration, manager.Generation())
			require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
			require.Equal(t, beforeBackend, backend.snapshot())
		})
	}
}

func TestManagerPropagatesStableIDErrors(t *testing.T) {
	t.Parallel()
	stableIDErr := errors.New("stable ID extraction failed")
	tests := []struct {
		name string
		run  func(*Manager[testUser]) error
	}{
		{
			name: "ApplyUsers",
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					Operations: []adapter.UserOperation[testUser]{
						{
							Type:  adapter.UserOperationAdd,
							ID:    "a",
							Value: testUser{ID: "a", Credential: "credential-a"},
						},
					},
				})
				return err
			},
		},
		{
			name: "ReplaceUsers",
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ReplaceUsers(
					context.Background(),
					0,
					"",
					"",
					[]testUser{{ID: "a", Credential: "credential-a"}},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := newFakeBackend()
			backend.setStableIDError(stableIDErr)
			manager := New[testUser](backend, Options{})

			err := test.run(manager)
			require.ErrorIs(t, err, ErrInvalidTransaction)
			require.ErrorIs(t, err, stableIDErr)
			require.Equal(t, adapter.UserGeneration(0), manager.Generation())
			require.Empty(t, canonicalTestRecords(manager))
			snapshot := backend.snapshot()
			require.Zero(t, snapshot.PrepareCalls)
			require.Zero(t, snapshot.CommitCalls)
			require.Empty(t, snapshot.Published)
		})
	}
}

func TestManagerRejectsNilPublishedState(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.setPrepareNil(true)
	manager := New[testUser](backend, Options{})

	_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "credential-a"},
			},
		},
	})
	require.ErrorIs(t, err, ErrBackendPrepareFailure)
	require.Equal(t, adapter.UserGeneration(0), manager.Generation())
	require.Empty(t, canonicalTestRecords(manager))
	snapshot := backend.snapshot()
	require.Equal(t, 1, snapshot.PrepareCalls)
	require.Zero(t, snapshot.CommitCalls)
	require.Empty(t, snapshot.Published)
}

func TestManagerApplyUsersAcceptsEmptyTransaction(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})

	result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{})
	require.NoError(t, err)
	require.Equal(t, adapter.UserTransactionResult{
		PreviousGeneration: 0,
		Generation:         1,
		Added:              []adapter.UserID{},
		Updated:            []adapter.UserID{},
		Deleted:            []adapter.UserID{},
	}, result)
	require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	snapshot := backend.snapshot()
	require.Equal(t, 1, snapshot.PrepareCalls)
	require.Equal(t, 1, snapshot.CommitCalls)
	require.Empty(t, snapshot.Published)
}

func TestManagerPostCommitHooksRecoverPanicsAndIgnoreNil(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	manager.AddPostCommitHook(nil)
	var panickingHookCalls atomic.Int32
	var laterHookCalls atomic.Int32
	manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
		panickingHookCalls.Add(1)
		panic("hook failed")
	})
	manager.AddPostCommitHook(func(adapter.UserTransactionResult) {
		laterHookCalls.Add(1)
	})

	result, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "a",
				Value: testUser{ID: "a", Credential: "credential-a"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(1), result.Generation)
	require.Equal(t, int32(1), panickingHookCalls.Load())
	require.Equal(t, int32(1), laterHookCalls.Load())
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "a", Credential: "credential-a"},
	))
}

func TestManagerReplaceUsersRejectsStaleGenerationBeforeInspectingUsers(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "a", Credential: "credential-a"})
	beforeStableIDCalls := backend.stableIDCalls.Load()
	beforeFingerprintCalls := backend.fingerprintCalls.Load()
	beforeBackend := backend.snapshot()
	beforeCanonical := canonicalTestRecords(manager)

	_, err := manager.ReplaceUsers(
		context.Background(),
		12345,
		"stale-replacement",
		"revision-stale",
		[]testUser{
			{ID: "a", Credential: "credential-a-rotated"},
			{ID: "b", Credential: "credential-b"},
		},
	)
	require.ErrorIs(t, err, ErrGenerationConflict)
	require.Equal(t, beforeStableIDCalls, backend.stableIDCalls.Load())
	require.Equal(t, beforeFingerprintCalls, backend.fingerprintCalls.Load())
	require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	require.Equal(t, beforeCanonical, canonicalTestRecords(manager))
	require.Equal(t, beforeBackend, backend.snapshot())
}

func TestManagerSkipsFingerprintsWithoutRequestID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*Manager[testUser]) error
	}{
		{
			name: "ApplyUsers",
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					Operations: []adapter.UserOperation[testUser]{
						{
							Type:  adapter.UserOperationAdd,
							ID:    "a",
							Value: testUser{ID: "a", Credential: "credential-a"},
						},
					},
				})
				return err
			},
		},
		{
			name: "ReplaceUsers",
			run: func(manager *Manager[testUser]) error {
				_, err := manager.ReplaceUsers(
					context.Background(),
					0,
					"",
					"revision-1",
					[]testUser{{ID: "a", Credential: "credential-a"}},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := newFakeBackend()
			manager := New[testUser](backend, Options{})

			err := test.run(manager)
			require.NoError(t, err)
			require.Zero(t, backend.fingerprintCalls.Load())
		})
	}
}

func TestManagerRejectsApplyReplayAfterCanonicalStateAdvances(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "alice", Credential: "credential-original"})
	revoke := adapter.UserTransaction[testUser]{
		RequestID: "revoke-alice",
		Operations: []adapter.UserOperation[testUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	}

	_, err := manager.ApplyUsers(context.Background(), revoke)
	require.NoError(t, err)
	_, err = manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "alice",
				Value: testUser{ID: "alice", Credential: "credential-restored"},
			},
		},
	})
	require.NoError(t, err)
	beforeBackend := backend.snapshot()

	_, err = manager.ApplyUsers(context.Background(), revoke)
	require.ErrorIs(t, err, ErrRequestIDConflict)
	require.ErrorContains(t, err, "canonical state advanced since the original commit")
	require.Equal(t, adapter.UserGeneration(3), manager.Generation())
	require.Equal(t, beforeBackend, backend.snapshot())
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "alice", Credential: "credential-restored"},
	))
}

func TestManagerRejectsReplaceReplayAfterCanonicalStateAdvances(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "alice", Credential: "credential-alice"})
	desired := []testUser{{ID: "alice", Credential: "credential-alice"}}

	_, err := manager.ReplaceUsers(
		context.Background(),
		0,
		"reconcile-users",
		"revision-1",
		desired,
	)
	require.NoError(t, err)
	_, err = manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "bob",
				Value: testUser{ID: "bob", Credential: "credential-bob"},
			},
		},
	})
	require.NoError(t, err)
	beforeBackend := backend.snapshot()

	_, err = manager.ReplaceUsers(
		context.Background(),
		0,
		"reconcile-users",
		"revision-1",
		desired,
	)
	require.ErrorIs(t, err, ErrRequestIDConflict)
	require.ErrorContains(t, err, "canonical state advanced since the original commit")
	require.Equal(t, adapter.UserGeneration(3), manager.Generation())
	require.Equal(t, beforeBackend, backend.snapshot())
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "alice", Credential: "credential-alice"},
		testUser{ID: "bob", Credential: "credential-bob"},
	))
}

func TestManagerRejectsEmptyReplacementReplayAfterCanonicalStateAdvances(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "alice", Credential: "credential-alice"})

	_, err := manager.ReplaceUsers(
		context.Background(),
		0,
		"purge-all",
		"revision-empty",
		[]testUser{},
	)
	require.NoError(t, err)
	_, err = manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "bob",
				Value: testUser{ID: "bob", Credential: "credential-bob"},
			},
		},
	})
	require.NoError(t, err)
	beforeBackend := backend.snapshot()

	_, err = manager.ReplaceUsers(
		context.Background(),
		0,
		"purge-all",
		"revision-empty",
		[]testUser{},
	)
	require.ErrorIs(t, err, ErrRequestIDConflict)
	require.ErrorContains(t, err, "canonical state advanced since the original commit")
	require.Equal(t, adapter.UserGeneration(3), manager.Generation())
	require.Equal(t, beforeBackend, backend.snapshot())
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "bob", Credential: "credential-bob"},
	))
}

func TestManagerRejectsExactStaleReplayBeforeCacheLookup(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	seedTestUsers(t, manager, testUser{ID: "alice", Credential: "credential-original"})
	revoke := adapter.UserTransaction[testUser]{
		ExpectedGeneration: 1,
		RequestID:          "revoke-alice-with-cas",
		Operations: []adapter.UserOperation[testUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	}

	_, err := manager.ApplyUsers(context.Background(), revoke)
	require.NoError(t, err)
	_, err = manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
		ExpectedGeneration: 2,
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "alice",
				Value: testUser{ID: "alice", Credential: "credential-restored"},
			},
		},
	})
	require.NoError(t, err)
	beforeBackend := backend.snapshot()

	_, err = manager.ApplyUsers(context.Background(), revoke)
	require.ErrorIs(t, err, ErrGenerationConflict)
	require.Equal(t, adapter.UserGeneration(3), manager.Generation())
	require.Equal(t, beforeBackend, backend.snapshot())
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "alice", Credential: "credential-restored"},
	))
}

func TestManagerChecksGenerationBeforeCachedReplay(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	manager := New[testUser](backend, Options{})
	transaction := adapter.UserTransaction[testUser]{
		RequestID: "cached-add",
		Operations: []adapter.UserOperation[testUser]{
			{
				Type:  adapter.UserOperationAdd,
				ID:    "alice",
				Value: testUser{ID: "alice", Credential: "credential-alice"},
			},
		},
	}

	_, err := manager.ApplyUsers(context.Background(), transaction)
	require.NoError(t, err)
	beforeFingerprintCalls := backend.fingerprintCalls.Load()
	beforeBackend := backend.snapshot()
	transaction.ExpectedGeneration = 12345

	_, err = manager.ApplyUsers(context.Background(), transaction)
	require.ErrorIs(t, err, ErrGenerationConflict)
	require.Equal(t, beforeFingerprintCalls, backend.fingerprintCalls.Load())
	require.Equal(t, adapter.UserGeneration(1), manager.Generation())
	require.Equal(t, beforeBackend, backend.snapshot())
	requireManagerState(t, manager, backend, testRecords(
		testUser{ID: "alice", Credential: "credential-alice"},
	))
}

func TestManagerReplaysEquivalentRequestsRegardlessOfOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *Manager[testUser]) (adapter.UserTransactionResult, adapter.UserTransactionResult)
	}{
		{
			name: "ApplyUsers operation order",
			run: func(t *testing.T, manager *Manager[testUser]) (
				adapter.UserTransactionResult,
				adapter.UserTransactionResult,
			) {
				t.Helper()
				operations := []adapter.UserOperation[testUser]{
					{
						Type:  adapter.UserOperationAdd,
						ID:    "a",
						Value: testUser{ID: "a", Credential: "credential-a"},
					},
					{
						Type:  adapter.UserOperationAdd,
						ID:    "b",
						Value: testUser{ID: "b", Credential: "credential-b"},
					},
				}
				first, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					RequestID:  "ordered-apply",
					Operations: operations,
				})
				require.NoError(t, err)
				reorderedOperations := slices.Clone(operations)
				slices.Reverse(reorderedOperations)
				second, err := manager.ApplyUsers(context.Background(), adapter.UserTransaction[testUser]{
					RequestID:  "ordered-apply",
					Operations: reorderedOperations,
				})
				require.NoError(t, err)
				return first, second
			},
		},
		{
			name: "ReplaceUsers input order",
			run: func(t *testing.T, manager *Manager[testUser]) (
				adapter.UserTransactionResult,
				adapter.UserTransactionResult,
			) {
				t.Helper()
				first, err := manager.ReplaceUsers(
					context.Background(),
					0,
					"ordered-replace",
					"revision-1",
					[]testUser{
						{ID: "a", Credential: "credential-a"},
						{ID: "b", Credential: "credential-b"},
					},
				)
				require.NoError(t, err)
				second, err := manager.ReplaceUsers(
					context.Background(),
					0,
					"ordered-replace",
					"revision-1",
					[]testUser{
						{ID: "b", Credential: "credential-b"},
						{ID: "a", Credential: "credential-a"},
					},
				)
				require.NoError(t, err)
				return first, second
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := newFakeBackend()
			manager := New[testUser](backend, Options{})

			first, second := test.run(t, manager)
			expectedReplay := first
			expectedReplay.Replayed = true
			require.Equal(t, expectedReplay, second)
			require.Equal(t, adapter.UserGeneration(1), manager.Generation())
			snapshot := backend.snapshot()
			require.Equal(t, 1, snapshot.PrepareCalls)
			require.Equal(t, 1, snapshot.CommitCalls)
			requireManagerState(t, manager, backend, testRecords(
				testUser{ID: "a", Credential: "credential-a"},
				testUser{ID: "b", Credential: "credential-b"},
			))
		})
	}
}
