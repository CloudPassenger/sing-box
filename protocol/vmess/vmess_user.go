package vmess

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-vmess"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	vmessFingerprintOffset64 uint64 = 14695981039346656037
	vmessFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.VMessUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.VMessUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.VMessUser] = (*Inbound)(nil)
)

type userBackend struct {
	service        *vmess.Service[adapter.UserID]
	unmanagedUsers []option.VMessUser
}

func newUserBackend(
	service *vmess.Service[adapter.UserID],
	unmanagedUsers []option.VMessUser,
) *userBackend {
	return &userBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *userBackend) StableID(user option.VMessUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty VMess user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.VMessUser) uint64 {
	fingerprint := fingerprintVMessString(vmessFingerprintOffset64, user.Name)
	fingerprint = fingerprintVMessString(fingerprint, user.UUID)
	return fingerprintVMessUint64(fingerprint, uint64(int64(user.AlterId)))
}

func (b *userBackend) Prepare(
	records []usermanager.Record[option.VMessUser],
) (usermanager.Published, error) {
	userCount := len(b.unmanagedUsers) + len(records)
	users := make([]adapter.UserID, 0, userCount)
	userIDs := make([]string, 0, userCount)
	alterIDs := make([]int, 0, userCount)

	for _, user := range b.unmanagedUsers {
		users = append(users, "")
		userIDs = append(userIDs, user.UUID)
		alterIDs = append(alterIDs, user.AlterId)
	}
	for _, record := range records {
		users = append(users, record.ID)
		userIDs = append(userIDs, record.Value.UUID)
		alterIDs = append(alterIDs, record.Value.AlterId)
	}

	prepared, err := b.service.PrepareUsers(users, userIDs, alterIDs)
	if err != nil {
		return nil, E.Cause(err, "prepare VMess users")
	}
	return &publishedUsers{
		service: b.service,
		users:   prepared,
	}, nil
}

type publishedUsers struct {
	service *vmess.Service[adapter.UserID]
	users   *vmess.PreparedUsers[adapter.UserID]
}

func (p *publishedUsers) Commit() {
	p.service.InstallUsers(p.users)
}

func fingerprintVMessString(fingerprint uint64, value string) uint64 {
	fingerprint = fingerprintVMessUint64(fingerprint, uint64(len(value)))
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * vmessFingerprintPrime64
	}
	return fingerprint
}

func fingerprintVMessUint64(fingerprint uint64, value uint64) uint64 {
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(value))) * vmessFingerprintPrime64
		value >>= 8
	}
	return fingerprint
}

func splitVMessUsers(users []option.VMessUser) ([]option.VMessUser, []option.VMessUser) {
	managedUsers := make([]option.VMessUser, 0, len(users))
	unmanagedUsers := make([]option.VMessUser, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, user)
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.VMessUser) error {
	managedUsers, unmanagedUsers := splitVMessUsers(users)
	manager := usermanager.New[option.VMessUser](
		newUserBackend(h.service, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize VMess managed users")
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.VMessUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.VMessUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
