package vmess

import (
	"context"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-vmess"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/gofrs/uuid/v5"
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
	uuidOwners := make(map[[16]byte]string, userCount)

	appendUser := func(userID adapter.UserID, user option.VMessUser, owner string) error {
		if user.AlterId < 0 {
			return E.New("invalid VMess alter ID for ", owner)
		}
		userUUID := vmessUserUUID(user.UUID)
		if previousOwner, loaded := uuidOwners[userUUID]; loaded {
			return E.New("duplicate VMess UUID credential for ", previousOwner, " and ", owner)
		}
		uuidOwners[userUUID] = owner
		users = append(users, userID)
		userIDs = append(userIDs, user.UUID)
		alterIDs = append(alterIDs, user.AlterId)
		return nil
	}

	for index, user := range b.unmanagedUsers {
		owner := fmt.Sprintf("unnamed static user #%d", index+1)
		if err := appendUser("", user, owner); err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		owner := fmt.Sprintf("user %q", record.ID)
		if err := appendUser(record.ID, record.Value, owner); err != nil {
			return nil, err
		}
	}

	return &publishedUsers{
		service:  b.service,
		users:    users,
		userIDs:  userIDs,
		alterIDs: alterIDs,
	}, nil
}

// vmessUserUUID mirrors the credential normalisation performed by the VMess
// service, so duplicate credentials are detected before publication.
func vmessUserUUID(value string) [16]byte {
	userUUID, err := uuid.FromString(value)
	if err != nil {
		userUUID = uuid.NewV5(uuid.Nil, value)
	}
	return userUUID
}

type publishedUsers struct {
	service  *vmess.Service[adapter.UserID]
	users    []adapter.UserID
	userIDs  []string
	alterIDs []int
}

func (p *publishedUsers) Commit() {
	// Prepare validated every credential, so publication cannot fail.
	_ = p.service.UpdateUsers(p.users, p.userIDs, p.alterIDs)
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
