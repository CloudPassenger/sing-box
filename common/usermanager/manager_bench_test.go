package usermanager

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

type benchmarkBackend struct {
	published []Record[testUser]
}

func (b *benchmarkBackend) StableID(user testUser) (adapter.UserID, error) {
	return user.ID, nil
}

func (b *benchmarkBackend) FingerprintUser(user testUser) uint64 {
	fingerprint := testFingerprintOffset64
	for index := range len(user.Credential) {
		fingerprint = (fingerprint ^ uint64(user.Credential[index])) * testFingerprintPrime64
	}
	return fingerprint
}

func (b *benchmarkBackend) Prepare(users []Record[testUser]) (Published, error) {
	return &benchmarkPublished{
		backend: b,
		records: slices.Clone(users),
	}, nil
}

type benchmarkPublished struct {
	backend *benchmarkBackend
	records []Record[testUser]
}

func (p *benchmarkPublished) Commit() {
	p.backend.published = p.records
}

var (
	_ Backend[testUser]       = (*benchmarkBackend)(nil)
	_ Fingerprinter[testUser] = (*benchmarkBackend)(nil)
)

func benchmarkUsers(userCount int, credentialRevision string) []testUser {
	users := make([]testUser, 0, userCount)
	for index := range userCount {
		users = append(users, testUser{
			ID:         adapter.UserID(fmt.Sprintf("user-%05d", index)),
			Credential: fmt.Sprintf("credential-%s-%05d", credentialRevision, index),
		})
	}
	return users
}

func benchmarkUpdateOperations(users []testUser) []adapter.UserOperation[testUser] {
	operations := make([]adapter.UserOperation[testUser], 0, len(users))
	for _, user := range users {
		operations = append(operations, adapter.UserOperation[testUser]{
			Type:  adapter.UserOperationUpdate,
			ID:    user.ID,
			Value: user,
		})
	}
	return operations
}

func newBenchmarkManager(
	b *testing.B,
	initialUsers []testUser,
) *Manager[testUser] {
	b.Helper()
	manager := New[testUser](&benchmarkBackend{}, Options{})
	_, err := manager.ReplaceUsers(
		context.Background(),
		0,
		"",
		"benchmark-initial",
		initialUsers,
	)
	if err != nil {
		b.Fatal(err)
	}
	return manager
}

func BenchmarkManagerSingleOperationDelta(b *testing.B) {
	for _, userCount := range []int{10, 1_000, 10_000} {
		b.Run(fmt.Sprintf("users=%d", userCount), func(b *testing.B) {
			firstUsers := benchmarkUsers(userCount, "a")
			secondUsers := benchmarkUsers(userCount, "b")
			manager := newBenchmarkManager(b, firstUsers)
			targetIndex := userCount / 2
			firstOperations := benchmarkUpdateOperations(firstUsers[targetIndex : targetIndex+1])
			secondOperations := benchmarkUpdateOperations(secondUsers[targetIndex : targetIndex+1])
			ctx := context.Background()
			useSecond := true

			b.ReportAllocs()
			for b.Loop() {
				operations := firstOperations
				if useSecond {
					operations = secondOperations
				}
				_, err := manager.ApplyUsers(ctx, adapter.UserTransaction[testUser]{
					Operations: operations,
				})
				if err != nil {
					b.Fatal(err)
				}
				useSecond = !useSecond
			}
		})
	}
}

func BenchmarkManagerBulkDelta(b *testing.B) {
	for _, userCount := range []int{10, 1_000, 10_000} {
		b.Run(fmt.Sprintf("users=%d", userCount), func(b *testing.B) {
			firstUsers := benchmarkUsers(userCount, "a")
			secondUsers := benchmarkUsers(userCount, "b")
			manager := newBenchmarkManager(b, firstUsers)
			firstOperations := benchmarkUpdateOperations(firstUsers)
			secondOperations := benchmarkUpdateOperations(secondUsers)
			ctx := context.Background()
			useSecond := true

			b.ReportAllocs()
			for b.Loop() {
				operations := firstOperations
				if useSecond {
					operations = secondOperations
				}
				_, err := manager.ApplyUsers(ctx, adapter.UserTransaction[testUser]{
					Operations: operations,
				})
				if err != nil {
					b.Fatal(err)
				}
				useSecond = !useSecond
			}
		})
	}
}

func BenchmarkManagerReplaceUsers(b *testing.B) {
	for _, userCount := range []int{10, 1_000, 10_000} {
		b.Run(fmt.Sprintf("users=%d", userCount), func(b *testing.B) {
			firstUsers := benchmarkUsers(userCount, "a")
			secondUsers := benchmarkUsers(userCount, "b")
			manager := newBenchmarkManager(b, firstUsers)
			ctx := context.Background()
			useSecond := true

			b.ReportAllocs()
			for b.Loop() {
				users := firstUsers
				if useSecond {
					users = secondUsers
				}
				_, err := manager.ReplaceUsers(
					ctx,
					0,
					"",
					"benchmark-replacement",
					users,
				)
				if err != nil {
					b.Fatal(err)
				}
				useSecond = !useSecond
			}
		})
	}
}
