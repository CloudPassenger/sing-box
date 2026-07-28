package vless

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	vmessvless "github.com/sagernet/sing-vmess/vless"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	vlessFingerprintOffset64 uint64 = 14695981039346656037
	vlessFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.VLESSUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.VLESSUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.VLESSUser] = (*Inbound)(nil)
)

type userBackend struct {
	service        *vmessvless.Service[adapter.UserID]
	unmanagedUsers []option.VLESSUser
}

func newUserBackend(
	service *vmessvless.Service[adapter.UserID],
	unmanagedUsers []option.VLESSUser,
) *userBackend {
	return &userBackend{
		service:        service,
		unmanagedUsers: append([]option.VLESSUser(nil), unmanagedUsers...),
	}
}

func (b *userBackend) StableID(user option.VLESSUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty VLESS user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.VLESSUser) uint64 {
	fingerprint := fingerprintVLESSString(vlessFingerprintOffset64, user.Name)
	fingerprint = fingerprintVLESSString(fingerprint, user.UUID)
	return fingerprintVLESSString(fingerprint, user.Flow)
}

func (b *userBackend) Prepare(
	records []usermanager.Record[option.VLESSUser],
) (usermanager.Published, error) {
	userCount := len(b.unmanagedUsers) + len(records)
	userIDs := make([]adapter.UserID, 0, userCount)
	userUUIDs := make([]string, 0, userCount)
	userFlows := make([]string, 0, userCount)
	for _, user := range b.unmanagedUsers {
		userIDs = append(userIDs, "")
		userUUIDs = append(userUUIDs, user.UUID)
		userFlows = append(userFlows, user.Flow)
	}
	for _, record := range records {
		userIDs = append(userIDs, record.ID)
		userUUIDs = append(userUUIDs, record.Value.UUID)
		userFlows = append(userFlows, record.Value.Flow)
	}
	users, err := b.service.PrepareUsers(userIDs, userUUIDs, userFlows)
	if err != nil {
		return nil, E.Cause(err, "compile VLESS users")
	}
	return &publishedUsers{
		service: b.service,
		users:   users,
	}, nil
}

type publishedUsers struct {
	service *vmessvless.Service[adapter.UserID]
	users   *vmessvless.PreparedUsers[adapter.UserID]
}

func (p *publishedUsers) Commit() {
	p.service.InstallUsers(p.users)
}

func fingerprintVLESSString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * vlessFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * vlessFingerprintPrime64
	}
	return fingerprint
}

func splitVLESSUsers(users []option.VLESSUser) ([]option.VLESSUser, []option.VLESSUser) {
	managedUsers := make([]option.VLESSUser, 0, len(users))
	unmanagedUsers := make([]option.VLESSUser, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, user)
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.VLESSUser) error {
	managedUsers, unmanagedUsers := splitVLESSUsers(users)
	manager := usermanager.New[option.VLESSUser](
		newUserBackend(h.service, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize VLESS managed users")
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.VLESSUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.VLESSUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
