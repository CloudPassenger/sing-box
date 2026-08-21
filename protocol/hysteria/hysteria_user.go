package hysteria

import (
	"context"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	hysteriaFingerprintOffset64 uint64 = 14695981039346656037
	hysteriaFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.HysteriaUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.HysteriaUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.HysteriaUser] = (*Inbound)(nil)
)

type unmanagedUser struct {
	password string
}

type userBackend struct {
	service        *hysteria.Service[adapter.UserID]
	unmanagedUsers []unmanagedUser
}

func newUserBackend(
	service *hysteria.Service[adapter.UserID],
	unmanagedUsers []unmanagedUser,
) *userBackend {
	return &userBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *userBackend) StableID(user option.HysteriaUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty Hysteria user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.HysteriaUser) uint64 {
	fingerprint := fingerprintHysteriaString(hysteriaFingerprintOffset64, user.Name)
	fingerprint = fingerprintHysteriaBytes(fingerprint, user.Auth)
	return fingerprintHysteriaString(fingerprint, user.AuthString)
}

func (b *userBackend) Prepare(
	records []usermanager.Record[option.HysteriaUser],
) (usermanager.Published, error) {
	userCount := len(b.unmanagedUsers) + len(records)
	userList := make([]adapter.UserID, 0, userCount)
	passwordList := make([]string, 0, userCount)
	passwordOwners := make(map[string]string, userCount)

	appendUser := func(userID adapter.UserID, password string, owner string) error {
		if previousOwner, loaded := passwordOwners[password]; loaded {
			return E.New("duplicate Hysteria authentication for ", previousOwner, " and ", owner)
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
		if err := appendUser(record.ID, hysteriaUserPassword(record.Value), owner); err != nil {
			return nil, err
		}
	}

	return &publishedUsers{
		service: b.service,
		state:   hysteria.NewUserState(userList, passwordList),
	}, nil
}

type publishedUsers struct {
	service *hysteria.Service[adapter.UserID]
	state   *hysteria.UserState[adapter.UserID]
}

func (p *publishedUsers) Commit() {
	p.service.UpdateUserState(p.state)
}

func fingerprintHysteriaBytes(fingerprint uint64, value []byte) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * hysteriaFingerprintPrime64
		length >>= 8
	}
	for _, currentByte := range value {
		fingerprint = (fingerprint ^ uint64(currentByte)) * hysteriaFingerprintPrime64
	}
	return fingerprint
}

func fingerprintHysteriaString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * hysteriaFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * hysteriaFingerprintPrime64
	}
	return fingerprint
}

func hysteriaUserPassword(user option.HysteriaUser) string {
	if user.AuthString != "" {
		return user.AuthString
	}
	return string(user.Auth)
}

func splitHysteriaUsers(users []option.HysteriaUser) ([]option.HysteriaUser, []unmanagedUser) {
	managedUsers := make([]option.HysteriaUser, 0, len(users))
	unmanagedUsers := make([]unmanagedUser, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, unmanagedUser{
				password: hysteriaUserPassword(user),
			})
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.HysteriaUser) error {
	managedUsers, unmanagedUsers := splitHysteriaUsers(users)
	manager := usermanager.New[option.HysteriaUser](
		newUserBackend(h.service, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize Hysteria managed users")
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.HysteriaUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.HysteriaUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
