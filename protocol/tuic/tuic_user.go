package tuic

import (
	"context"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/tuic"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/gofrs/uuid/v5"
)

const (
	tuicFingerprintOffset64 uint64 = 14695981039346656037
	tuicFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.TUICUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.TUICUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.TUICUser] = (*Inbound)(nil)
)

type unmanagedUser struct {
	uuid     string
	password string
}

type userBackend struct {
	service        *tuic.Service[adapter.UserID]
	unmanagedUsers []unmanagedUser
}

func newUserBackend(
	service *tuic.Service[adapter.UserID],
	unmanagedUsers []unmanagedUser,
) *userBackend {
	return &userBackend{
		service:        service,
		unmanagedUsers: unmanagedUsers,
	}
}

func (b *userBackend) StableID(user option.TUICUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty TUIC user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.TUICUser) uint64 {
	fingerprint := fingerprintTUICString(tuicFingerprintOffset64, user.Name)
	fingerprint = fingerprintTUICString(fingerprint, user.UUID)
	return fingerprintTUICString(fingerprint, user.Password)
}

func (b *userBackend) Prepare(
	records []usermanager.Record[option.TUICUser],
) (usermanager.Published, error) {
	userCount := len(b.unmanagedUsers) + len(records)
	userList := make([]adapter.UserID, 0, userCount)
	uuidList := make([][16]byte, 0, userCount)
	passwordList := make([]string, 0, userCount)
	uuidOwners := make(map[[16]byte]string, userCount)

	appendUser := func(userID adapter.UserID, uuidString string, password string, owner string) error {
		if uuidString == "" {
			return E.New("missing UUID for ", owner)
		}
		parsedUUID, err := uuid.FromString(uuidString)
		if err != nil {
			return E.New("invalid UUID for ", owner)
		}
		userUUID := [16]byte(parsedUUID)
		if previousOwner, loaded := uuidOwners[userUUID]; loaded {
			return E.New("duplicate TUIC UUID for ", previousOwner, " and ", owner)
		}
		uuidOwners[userUUID] = owner
		userList = append(userList, userID)
		uuidList = append(uuidList, userUUID)
		passwordList = append(passwordList, password)
		return nil
	}

	for index, user := range b.unmanagedUsers {
		owner := fmt.Sprintf("unnamed static user #%d", index+1)
		if err := appendUser("", user.uuid, user.password, owner); err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		owner := fmt.Sprintf("user %q", record.ID)
		if err := appendUser(record.ID, record.Value.UUID, record.Value.Password, owner); err != nil {
			return nil, err
		}
	}

	return &publishedUsers{
		service: b.service,
		state:   tuic.NewUserState(userList, uuidList, passwordList),
	}, nil
}

type publishedUsers struct {
	service *tuic.Service[adapter.UserID]
	state   *tuic.UserState[adapter.UserID]
}

func (p *publishedUsers) Commit() {
	p.service.UpdateUserState(p.state)
}

func fingerprintTUICString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * tuicFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * tuicFingerprintPrime64
	}
	return fingerprint
}

func splitTUICUsers(users []option.TUICUser) ([]option.TUICUser, []unmanagedUser) {
	managedUsers := make([]option.TUICUser, 0, len(users))
	unmanagedUsers := make([]unmanagedUser, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedUsers = append(unmanagedUsers, unmanagedUser{
				uuid:     user.UUID,
				password: user.Password,
			})
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedUsers
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.TUICUser) error {
	managedUsers, unmanagedUsers := splitTUICUsers(users)
	manager := usermanager.New[option.TUICUser](
		newUserBackend(h.server, unmanagedUsers),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize TUIC managed users")
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.TUICUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.TUICUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
