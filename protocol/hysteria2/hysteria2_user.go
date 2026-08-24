package hysteria2

import (
	"context"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria2"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	hysteria2FingerprintOffset64 uint64 = 14695981039346656037
	hysteria2FingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.Hysteria2User]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.Hysteria2User]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.Hysteria2User] = (*Inbound)(nil)
)

type unmanagedUser struct {
	password string
}

type userBackend struct {
	service        *hysteria2.Service[adapter.UserID]
	unmanagedUsers []unmanagedUser
}

func newUserBackend(
	service *hysteria2.Service[adapter.UserID],
	unmanagedUsers []unmanagedUser,
) *userBackend {
	return &userBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *userBackend) StableID(user option.Hysteria2User) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty Hysteria2 user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.Hysteria2User) uint64 {
	fingerprint := fingerprintHysteria2String(hysteria2FingerprintOffset64, user.Name)
	return fingerprintHysteria2String(fingerprint, user.Password)
}

func (b *userBackend) Prepare(
	records []usermanager.Record[option.Hysteria2User],
) (usermanager.Published, error) {
	userCount := len(b.unmanagedUsers) + len(records)
	userList := make([]adapter.UserID, 0, userCount)
	passwordList := make([]string, 0, userCount)
	passwordOwners := make(map[string]string, userCount)

	appendUser := func(userID adapter.UserID, password string, owner string) error {
		if previousOwner, loaded := passwordOwners[password]; loaded {
			return E.New("duplicate Hysteria2 password for ", previousOwner, " and ", owner)
		}
		passwordOwners[password] = owner
		userList = append(userList, userID)
		passwordList = append(passwordList, password)
		return nil
	}

	for index, user := range b.unmanagedUsers {
		owner := fmt.Sprintf("unnamed static user #%d", index+1)
		if err := appendUser("", user.password, owner); err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		owner := fmt.Sprintf("user %q", record.ID)
		if err := appendUser(record.ID, record.Value.Password, owner); err != nil {
			return nil, err
		}
	}

	return &publishedUsers{
		service:      b.service,
		userList:     userList,
		passwordList: passwordList,
	}, nil
}

type publishedUsers struct {
	service      *hysteria2.Service[adapter.UserID]
	userList     []adapter.UserID
	passwordList []string
}

func (p *publishedUsers) Commit() {
	p.service.UpdateUsers(p.userList, p.passwordList)
}

func fingerprintHysteria2String(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * hysteria2FingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * hysteria2FingerprintPrime64
	}
	return fingerprint
}

func splitHysteria2Users(users []option.Hysteria2User) ([]option.Hysteria2User, []unmanagedUser) {
	managedUsers := make([]option.Hysteria2User, 0, len(users))
	unmanagedUsers := make([]unmanagedUser, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, unmanagedUser{password: user.Password})
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.Hysteria2User) error {
	managedUsers, unmanagedUsers := splitHysteria2Users(users)
	manager := usermanager.New[option.Hysteria2User](
		newUserBackend(h.service, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize Hysteria2 managed users")
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.Hysteria2User],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.Hysteria2User,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
