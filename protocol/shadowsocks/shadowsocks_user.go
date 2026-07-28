package shadowsocks

import (
	"context"
	"strconv"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	shadowsocksFingerprintOffset64 uint64 = 14695981039346656037
	shadowsocksFingerprintPrime64  uint64 = 1099511628211
	shadowsocksStaticUserIDPrefix         = "\x00sing-box:shadowsocks:static:"
)

var (
	_ usermanager.Backend[option.ShadowsocksUser]        = (*shadowsocksUserBackend)(nil)
	_ usermanager.Fingerprinter[option.ShadowsocksUser]  = (*shadowsocksUserBackend)(nil)
	_ adapter.ManagedUserManager[option.ShadowsocksUser] = (*MultiInbound)(nil)
)

type unmanagedShadowsocksUser struct {
	id       adapter.UserID
	password string
}

type shadowsocksUserBackend struct {
	service        *shadowaead_2022.MultiService[adapter.UserID]
	unmanagedUsers []unmanagedShadowsocksUser
}

func newShadowsocksUserBackend(
	service *shadowaead_2022.MultiService[adapter.UserID],
	unmanagedUsers []unmanagedShadowsocksUser,
) *shadowsocksUserBackend {
	return &shadowsocksUserBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *shadowsocksUserBackend) StableID(user option.ShadowsocksUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty Shadowsocks user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *shadowsocksUserBackend) FingerprintUser(user option.ShadowsocksUser) uint64 {
	fingerprint := fingerprintShadowsocksString(shadowsocksFingerprintOffset64, user.Name)
	return fingerprintShadowsocksString(fingerprint, user.Password)
}

func (b *shadowsocksUserBackend) Prepare(
	records []usermanager.Record[option.ShadowsocksUser],
) (usermanager.Published, error) {
	userCount := len(b.unmanagedUsers) + len(records)
	userIDs := make([]adapter.UserID, 0, userCount)
	passwords := make([]string, 0, userCount)
	for _, user := range b.unmanagedUsers {
		userIDs = append(userIDs, user.id)
		passwords = append(passwords, user.password)
	}
	for _, record := range records {
		userIDs = append(userIDs, record.ID)
		passwords = append(passwords, record.Value.Password)
	}

	users, err := b.service.PrepareUsersWithPasswords(userIDs, passwords)
	if err != nil {
		return nil, E.Cause(err, "compile Shadowsocks 2022 users")
	}
	return &publishedShadowsocksUsers{
		service: b.service,
		users:   users,
	}, nil
}

type publishedShadowsocksUsers struct {
	service *shadowaead_2022.MultiService[adapter.UserID]
	users   *shadowaead_2022.PreparedUsers[adapter.UserID]
}

func (p *publishedShadowsocksUsers) Commit() {
	p.service.PublishUsers(p.users)
}

func splitShadowsocksUsers(
	users []option.ShadowsocksUser,
) (
	managedUsers []option.ShadowsocksUser,
	unmanagedUsers []unmanagedShadowsocksUser,
	unmanagedUserLabels map[adapter.UserID]string,
) {
	managedUsers = make([]option.ShadowsocksUser, 0, len(users))
	unmanagedUsers = make([]unmanagedShadowsocksUser, 0, len(users))
	unmanagedUserLabels = make(map[adapter.UserID]string, len(users))
	for index, user := range users {
		if user.Name != "" {
			managedUsers = append(managedUsers, user)
			continue
		}
		userID := shadowsocksStaticUserID(index)
		unmanagedUsers = append(unmanagedUsers, unmanagedShadowsocksUser{
			id:       userID,
			password: user.Password,
		})
		unmanagedUserLabels[userID] = strconv.Itoa(index)
	}
	return
}

func shadowsocksStaticUserID(index int) adapter.UserID {
	return adapter.UserID(shadowsocksStaticUserIDPrefix + strconv.Itoa(index))
}

func fingerprintShadowsocksString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * shadowsocksFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * shadowsocksFingerprintPrime64
	}
	return fingerprint
}

func (h *MultiInbound) initializeUserManager(ctx context.Context, users []option.ShadowsocksUser) error {
	managedUsers, unmanagedUsers, unmanagedUserLabels := splitShadowsocksUsers(users)
	manager := usermanager.New[option.ShadowsocksUser](
		newShadowsocksUserBackend(h.managedService, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize Shadowsocks managed users")
	}
	h.userManager = manager
	h.unmanagedUserLabels = unmanagedUserLabels
	return nil
}

func (h *MultiInbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *MultiInbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.ShadowsocksUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *MultiInbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.ShadowsocksUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
