package snell

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// fakeMultiService records the generation installed by publishedUsers.Commit.
type fakeMultiService struct {
	users []adapter.UserID
	keys  [][]byte
	calls int
}

func (f *fakeMultiService) UpdateUsers(users []adapter.UserID, userKeys [][]byte) error {
	f.calls++
	f.users = users
	f.keys = userKeys
	return nil
}

func (f *fakeMultiService) installed() map[string]string {
	state := make(map[string]string, len(f.users))
	for index, user := range f.users {
		state[string(user)] = string(f.keys[index])
	}
	return state
}

func TestSnellUserBackendIdentityAndValidation(t *testing.T) {
	t.Parallel()

	backend := newUserBackend(&fakeMultiService{}, []option.SnellUser{
		{UserKey: "legacy-unnamed-key"},
	})

	stableID, err := backend.StableID(option.SnellUser{Name: "alice", UserKey: "alice-key"})
	require.NoError(t, err)
	require.Equal(t, adapter.UserID("alice"), stableID)

	_, err = backend.StableID(option.SnellUser{UserKey: "alice-key"})
	require.ErrorContains(t, err, "empty Snell user name")

	baseFingerprint := backend.FingerprintUser(option.SnellUser{Name: "alice", UserKey: "alice-key"})
	require.NotEqual(t, baseFingerprint, backend.FingerprintUser(option.SnellUser{
		Name:    "bob",
		UserKey: "alice-key",
	}))
	require.NotEqual(t, baseFingerprint, backend.FingerprintUser(option.SnellUser{
		Name:    "alice",
		UserKey: "rotated-key",
	}))

	// A managed user colliding with the static user must not be published.
	published, err := backend.Prepare([]usermanager.Record[option.SnellUser]{
		{ID: "alice", Value: option.SnellUser{Name: "alice", UserKey: "legacy-unnamed-key"}},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "duplicate Snell user key")

	published, err = backend.Prepare([]usermanager.Record[option.SnellUser]{
		{ID: "alice", Value: option.SnellUser{Name: "alice", UserKey: ""}},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "empty Snell user key")

	published, err = backend.Prepare([]usermanager.Record[option.SnellUser]{
		{ID: "alice", Value: option.SnellUser{Name: "alice", UserKey: strings.Repeat("k", 256)}},
	})
	require.Nil(t, published)
	require.ErrorContains(t, err, "snell user key too long")
}

func TestSnellUserBackendRefusesEmptyGeneration(t *testing.T) {
	t.Parallel()

	// Upstream MultiService has no top-level PSK fallback and rejects an empty
	// user map, so publishing one would silently lock every client out.
	backend := newUserBackend(&fakeMultiService{}, nil)
	published, err := backend.Prepare(nil)
	require.Nil(t, published)
	require.ErrorContains(t, err, "at least one user")
}

func TestSnellUserBackendPublishesMergedGeneration(t *testing.T) {
	t.Parallel()

	service := &fakeMultiService{}
	backend := newUserBackend(service, []option.SnellUser{
		{UserKey: "legacy-unnamed-key"},
	})

	published, err := backend.Prepare([]usermanager.Record[option.SnellUser]{
		{ID: "alice", Value: option.SnellUser{Name: "alice", UserKey: "alice-key"}},
		{ID: "bob", Value: option.SnellUser{Name: "bob", UserKey: "bob-key"}},
	})
	require.NoError(t, err)
	require.Equal(t, 0, service.calls, "Prepare must not mutate the service")

	published.Commit()
	require.Equal(t, 1, service.calls)
	require.Equal(t, map[string]string{
		"unnamed static user #1": "legacy-unnamed-key",
		"alice":                  "alice-key",
		"bob":                    "bob-key",
	}, service.installed())
}

func TestSnellManagedUsersLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := &fakeMultiService{}
	inbound := &Inbound{multiService: service}
	require.NoError(t, inbound.initializeUserManager(ctx, []option.SnellUser{
		{UserKey: "legacy-unnamed-key"},
		{Name: "alice", UserKey: "alice-key"},
	}))
	require.Equal(t, adapter.UserGeneration(1), inbound.Generation())
	require.Equal(t, map[string]string{
		"unnamed static user #1": "legacy-unnamed-key",
		"alice":                  "alice-key",
	}, service.installed())

	addResult, err := inbound.ApplyUsers(ctx, adapter.UserTransaction[option.SnellUser]{
		ExpectedGeneration: inbound.Generation(),
		Operations: []adapter.UserOperation[option.SnellUser]{
			{Type: adapter.UserOperationAdd, ID: "bob", Value: option.SnellUser{Name: "bob", UserKey: "bob-key"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, adapter.UserGeneration(2), addResult.Generation)
	require.Equal(t, []adapter.UserID{"bob"}, addResult.Added)
	require.Equal(t, "bob-key", service.installed()["bob"])

	// A rotation onto an already used key must be rejected without publishing.
	beforeConflict := service.installed()
	_, err = inbound.ApplyUsers(ctx, adapter.UserTransaction[option.SnellUser]{
		ExpectedGeneration: inbound.Generation(),
		Operations: []adapter.UserOperation[option.SnellUser]{
			{Type: adapter.UserOperationUpdate, ID: "bob", Value: option.SnellUser{Name: "bob", UserKey: "alice-key"}},
		},
	})
	require.ErrorIs(t, err, usermanager.ErrBackendPrepareFailure)
	require.Equal(t, adapter.UserGeneration(2), inbound.Generation())
	require.Equal(t, beforeConflict, service.installed())

	rotateResult, err := inbound.ApplyUsers(ctx, adapter.UserTransaction[option.SnellUser]{
		ExpectedGeneration: inbound.Generation(),
		Operations: []adapter.UserOperation[option.SnellUser]{
			{Type: adapter.UserOperationUpdate, ID: "alice", Value: option.SnellUser{Name: "alice", UserKey: "alice-rotated"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"alice"}, rotateResult.Updated)
	require.Equal(t, "alice-rotated", service.installed()["alice"])

	deleteResult, err := inbound.ApplyUsers(ctx, adapter.UserTransaction[option.SnellUser]{
		ExpectedGeneration: inbound.Generation(),
		Operations: []adapter.UserOperation[option.SnellUser]{
			{Type: adapter.UserOperationDelete, ID: "alice"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []adapter.UserID{"alice"}, deleteResult.Deleted)
	require.NotContains(t, service.installed(), "alice")

	// The static user keeps the generation non-empty, so deleting every managed
	// user still publishes successfully.
	_, err = inbound.ApplyUsers(ctx, adapter.UserTransaction[option.SnellUser]{
		ExpectedGeneration: inbound.Generation(),
		Operations: []adapter.UserOperation[option.SnellUser]{
			{Type: adapter.UserOperationDelete, ID: "bob"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"unnamed static user #1": "legacy-unnamed-key",
	}, service.installed())
}
