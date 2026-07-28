package shadowsocks

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	shadowsocksFingerprintOffset64 uint64 = 14695981039346656037
	shadowsocksFingerprintPrime64  uint64 = 1099511628211
	shadowsocksStaticUserIDPrefix         = "\x00sing-box:shadowsocks:static:"
)

var (
	_ usermanager.Backend[option.ShadowsocksUser]        = (*shadowsocks2022UserBackend)(nil)
	_ usermanager.Fingerprinter[option.ShadowsocksUser]  = (*shadowsocks2022UserBackend)(nil)
	_ usermanager.Backend[option.ShadowsocksUser]        = (*legacyShadowsocksUserBackend)(nil)
	_ usermanager.Fingerprinter[option.ShadowsocksUser]  = (*legacyShadowsocksUserBackend)(nil)
	_ adapter.ManagedUserManager[option.ShadowsocksUser] = (*MultiInbound)(nil)
)

type unmanagedShadowsocksUser struct {
	id       adapter.UserID
	password string
}

type shadowsocksUserCandidate struct {
	id       adapter.UserID
	password string
}

type shadowsocksUserIdentity struct{}

func (shadowsocksUserIdentity) StableID(user option.ShadowsocksUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty Shadowsocks user name")
	}
	return adapter.UserID(user.Name), nil
}

func (shadowsocksUserIdentity) FingerprintUser(user option.ShadowsocksUser) uint64 {
	fingerprint := fingerprintShadowsocksString(shadowsocksFingerprintOffset64, user.Name)
	return fingerprintShadowsocksString(fingerprint, user.Password)
}

type shadowsocks2022UserBackend struct {
	shadowsocksUserIdentity
	service        *shadowaead_2022.MultiService[adapter.UserID]
	unmanagedUsers []unmanagedShadowsocksUser
}

func newShadowsocks2022UserBackend(
	service *shadowaead_2022.MultiService[adapter.UserID],
	unmanagedUsers []unmanagedShadowsocksUser,
) *shadowsocks2022UserBackend {
	return &shadowsocks2022UserBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *shadowsocks2022UserBackend) Prepare(
	records []usermanager.Record[option.ShadowsocksUser],
) (usermanager.Published, error) {
	userIDs, passwords := mergeShadowsocksUsers(b.unmanagedUsers, records)
	users, err := b.service.PrepareUsersWithPasswords(userIDs, passwords)
	if err != nil {
		return nil, E.Cause(err, "compile Shadowsocks 2022 users")
	}
	return &publishedShadowsocks2022Users{
		service: b.service,
		users:   users,
	}, nil
}

type publishedShadowsocks2022Users struct {
	service *shadowaead_2022.MultiService[adapter.UserID]
	users   *shadowaead_2022.PreparedUsers[adapter.UserID]
}

func (p *publishedShadowsocks2022Users) Commit() {
	p.service.PublishUsers(p.users)
}

type legacyShadowsocksUserBackend struct {
	shadowsocksUserIdentity
	service        *shadowaead.MultiService[adapter.UserID]
	unmanagedUsers []unmanagedShadowsocksUser
}

func newLegacyShadowsocksUserBackend(
	service *shadowaead.MultiService[adapter.UserID],
	unmanagedUsers []unmanagedShadowsocksUser,
) *legacyShadowsocksUserBackend {
	return &legacyShadowsocksUserBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *legacyShadowsocksUserBackend) Prepare(
	records []usermanager.Record[option.ShadowsocksUser],
) (usermanager.Published, error) {
	userIDs, passwords := mergeShadowsocksUsers(b.unmanagedUsers, records)
	users, err := b.service.PrepareUsersWithPasswords(userIDs, passwords)
	if err != nil {
		return nil, E.Cause(err, "compile legacy Shadowsocks users")
	}
	return &publishedLegacyShadowsocksUsers{
		service: b.service,
		users:   users,
	}, nil
}

type publishedLegacyShadowsocksUsers struct {
	service *shadowaead.MultiService[adapter.UserID]
	users   *shadowaead.PreparedUsers[adapter.UserID]
}

func (p *publishedLegacyShadowsocksUsers) Commit() {
	p.service.PublishUsers(p.users)
}

func mergeShadowsocksUsers(
	unmanagedUsers []unmanagedShadowsocksUser,
	records []usermanager.Record[option.ShadowsocksUser],
) ([]adapter.UserID, []string) {
	candidates := make([]shadowsocksUserCandidate, 0, len(unmanagedUsers)+len(records))
	for _, user := range unmanagedUsers {
		candidates = append(candidates, shadowsocksUserCandidate(user))
	}
	for _, record := range records {
		candidates = append(candidates, shadowsocksUserCandidate{
			id:       record.ID,
			password: record.Value.Password,
		})
	}
	slices.SortFunc(candidates, func(left, right shadowsocksUserCandidate) int {
		return cmp.Compare(left.id, right.id)
	})

	userIDs := make([]adapter.UserID, len(candidates))
	passwords := make([]string, len(candidates))
	for index, candidate := range candidates {
		userIDs[index] = candidate.id
		passwords[index] = candidate.password
	}
	return userIDs, passwords
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

func (h *MultiInbound) initialize2022UserManager(
	ctx context.Context,
	service *shadowaead_2022.MultiService[adapter.UserID],
	users []option.ShadowsocksUser,
) error {
	managedUsers, unmanagedUsers, unmanagedUserLabels := splitShadowsocksUsers(users)
	return h.initializeUserManager(
		ctx,
		managedUsers,
		unmanagedUserLabels,
		newShadowsocks2022UserBackend(service, unmanagedUsers),
	)
}

func (h *MultiInbound) initializeLegacyUserManager(
	ctx context.Context,
	service *shadowaead.MultiService[adapter.UserID],
	users []option.ShadowsocksUser,
) error {
	managedUsers, unmanagedUsers, unmanagedUserLabels := splitShadowsocksUsers(users)
	return h.initializeUserManager(
		ctx,
		managedUsers,
		unmanagedUserLabels,
		newLegacyShadowsocksUserBackend(service, unmanagedUsers),
	)
}

func (h *MultiInbound) initializeUserManager(
	ctx context.Context,
	managedUsers []option.ShadowsocksUser,
	unmanagedUserLabels map[adapter.UserID]string,
	backend usermanager.Backend[option.ShadowsocksUser],
) error {
	manager := usermanager.New[option.ShadowsocksUser](backend, usermanager.Options{})
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
