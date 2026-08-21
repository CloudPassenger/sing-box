package anytls

import (
	"context"
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"

	anytls "github.com/anytls/sing-anytls"
)

const (
	anyTLSFingerprintOffset64 uint64 = 14695981039346656037
	anyTLSFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.AnyTLSUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.AnyTLSUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.AnyTLSUser] = (*Inbound)(nil)
)

type userBackend struct {
	service        *anytls.Service
	unmanagedUsers []anytls.User
}

func newUserBackend(service *anytls.Service, unmanagedUsers []anytls.User) *userBackend {
	return &userBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *userBackend) StableID(user option.AnyTLSUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", errors.New("empty AnyTLS user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.AnyTLSUser) uint64 {
	fingerprint := fingerprintAnyTLSString(anyTLSFingerprintOffset64, user.Name)
	return fingerprintAnyTLSString(fingerprint, user.Password)
}

func (b *userBackend) Prepare(records []usermanager.Record[option.AnyTLSUser]) (usermanager.Published, error) {
	users := make([]anytls.User, 0, len(b.unmanagedUsers)+len(records))
	passwordOwners := make(map[string]string, cap(users))

	appendUser := func(user anytls.User, owner string) error {
		if previousOwner, loaded := passwordOwners[user.Password]; loaded {
			return fmt.Errorf("duplicate AnyTLS password for %s and %s", previousOwner, owner)
		}
		passwordOwners[user.Password] = owner
		users = append(users, user)
		return nil
	}

	for index, user := range b.unmanagedUsers {
		owner := fmt.Sprintf("unnamed static user #%d", index+1)
		if err := appendUser(user, owner); err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		name := string(record.ID)
		if err := appendUser(anytls.User{
			Name:     name,
			Password: record.Value.Password,
		}, fmt.Sprintf("user %q", name)); err != nil {
			return nil, err
		}
	}

	return &publishedUsers{
		service: b.service,
		users:   users,
	}, nil
}

type publishedUsers struct {
	service *anytls.Service
	users   []anytls.User
}

func (p *publishedUsers) Commit() {
	p.service.UpdateUsers(p.users)
}

func fingerprintAnyTLSString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * anyTLSFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * anyTLSFingerprintPrime64
	}
	return fingerprint
}

func splitAnyTLSUsers(users []option.AnyTLSUser) ([]option.AnyTLSUser, []anytls.User) {
	managedUsers := make([]option.AnyTLSUser, 0, len(users))
	unmanagedUsers := make([]anytls.User, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, anytls.User(user))
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.AnyTLSUser) error {
	managedUsers, unmanagedUsers := splitAnyTLSUsers(users)
	manager := usermanager.New[option.AnyTLSUser](
		newUserBackend(h.service, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return fmt.Errorf("initialize AnyTLS managed users: %w", err)
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.AnyTLSUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.AnyTLSUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
