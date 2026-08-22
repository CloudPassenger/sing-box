package snell

import (
	"context"
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
)

const (
	snellFingerprintOffset64 uint64 = 14695981039346656037
	snellFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.SnellUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.SnellUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.SnellUser] = (*Inbound)(nil)
)

// snellMultiService is the subset of snellv5.MultiService and snellv6.MultiService
// the managed user backend needs. Snell exposes a single-phase setter instead of
// the prepare/install pair the other protocols use, so this backend validates the
// whole generation in Prepare and only swaps it in Commit. The pinned fork
// publishes that swap atomically, so an authenticating connection can never
// observe a torn user table.
type snellMultiService interface {
	UpdateUsers(users []adapter.UserID, userKeys [][]byte) error
}

type userBackend struct {
	service         snellMultiService
	unmanagedKeys   [][]byte
	unmanagedLabels []string
}

func newUserBackend(service snellMultiService, unmanagedUsers []option.SnellUser) *userBackend {
	keys := make([][]byte, 0, len(unmanagedUsers))
	labels := make([]string, 0, len(unmanagedUsers))
	for index, user := range unmanagedUsers {
		keys = append(keys, []byte(user.UserKey))
		labels = append(labels, fmt.Sprintf("unnamed static user #%d", index+1))
	}
	return &userBackend{
		service:         service,
		unmanagedKeys:   keys,
		unmanagedLabels: labels,
	}
}

func (b *userBackend) StableID(user option.SnellUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", errors.New("empty Snell user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.SnellUser) uint64 {
	fingerprint := fingerprintSnellString(snellFingerprintOffset64, user.Name)
	return fingerprintSnellString(fingerprint, user.UserKey)
}

func (b *userBackend) Prepare(records []usermanager.Record[option.SnellUser]) (usermanager.Published, error) {
	total := len(b.unmanagedKeys) + len(records)
	users := make([]adapter.UserID, 0, total)
	keys := make([][]byte, 0, total)
	keyOwners := make(map[string]string, total)

	appendUser := func(id adapter.UserID, key []byte, owner string) error {
		if len(key) == 0 {
			return fmt.Errorf("empty Snell user key for %s", owner)
		}
		if len(key) > 255 {
			return fmt.Errorf("snell user key too long for %s", owner)
		}
		if previousOwner, loaded := keyOwners[string(key)]; loaded {
			return fmt.Errorf("duplicate Snell user key for %s and %s", previousOwner, owner)
		}
		keyOwners[string(key)] = owner
		users = append(users, id)
		keys = append(keys, key)
		return nil
	}

	for index, key := range b.unmanagedKeys {
		if err := appendUser(adapter.UserID(b.unmanagedLabels[index]), key, b.unmanagedLabels[index]); err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		name := string(record.ID)
		if err := appendUser(record.ID, []byte(record.Value.UserKey), fmt.Sprintf("user %q", name)); err != nil {
			return nil, err
		}
	}

	// Upstream MultiService authenticates solely from its user map and rejects an
	// empty generation, so refuse it here instead of publishing a server that
	// accepts nobody.
	if len(users) == 0 {
		return nil, errors.New("snell managed users must keep at least one user; the multi-user service has no top-level PSK fallback")
	}

	return &publishedUsers{
		service: b.service,
		users:   users,
		keys:    keys,
	}, nil
}

type publishedUsers struct {
	service snellMultiService
	users   []adapter.UserID
	keys    [][]byte
}

func (p *publishedUsers) Commit() {
	// Prepare already enforced every condition UpdateUsers validates, so the
	// remaining call cannot fail.
	_ = p.service.UpdateUsers(p.users, p.keys)
}

func fingerprintSnellString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * snellFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * snellFingerprintPrime64
	}
	return fingerprint
}

func splitSnellUsers(users []option.SnellUser) ([]option.SnellUser, []option.SnellUser) {
	managedUsers := make([]option.SnellUser, 0, len(users))
	unmanagedUsers := make([]option.SnellUser, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, user)
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.SnellUser) error {
	managedUsers, unmanagedUsers := splitSnellUsers(users)
	manager := usermanager.New[option.SnellUser](
		newUserBackend(h.multiService, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return fmt.Errorf("initialize Snell managed users: %w", err)
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.SnellUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.SnellUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(ctx, expectedGeneration, requestID, sourceRevision, users)
}
