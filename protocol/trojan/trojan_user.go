package trojan

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/usermanager"
	"github.com/sagernet/sing-box/option"
	trojanTransport "github.com/sagernet/sing-box/transport/trojan"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	trojanFingerprintOffset64 uint64 = 14695981039346656037
	trojanFingerprintPrime64  uint64 = 1099511628211
)

var (
	_ usermanager.Backend[option.TrojanUser]        = (*userBackend)(nil)
	_ usermanager.Fingerprinter[option.TrojanUser]  = (*userBackend)(nil)
	_ adapter.ManagedUserManager[option.TrojanUser] = (*Inbound)(nil)
)

type userBackend struct {
	service            *trojanTransport.Service[adapter.UserID]
	unmanagedPasswords []string
}

func newUserBackend(service *trojanTransport.Service[adapter.UserID], unmanagedPasswords []string) *userBackend {
	return &userBackend{
		service:            service,
		unmanagedPasswords: unmanagedPasswords,
	}
}

func (b *userBackend) StableID(user option.TrojanUser) (adapter.UserID, error) {
	if user.Name == "" {
		return "", E.New("empty Trojan user name")
	}
	return adapter.UserID(user.Name), nil
}

func (b *userBackend) FingerprintUser(user option.TrojanUser) uint64 {
	fingerprint := fingerprintTrojanString(trojanFingerprintOffset64, user.Name)
	return fingerprintTrojanString(fingerprint, user.Password)
}

func (b *userBackend) Prepare(records []usermanager.Record[option.TrojanUser]) (usermanager.Published, error) {
	userCount := len(b.unmanagedPasswords) + len(records)
	users := make([]adapter.UserID, 0, userCount)
	passwords := make([]string, 0, userCount)
	for _, password := range b.unmanagedPasswords {
		users = append(users, "")
		passwords = append(passwords, password)
	}
	for _, record := range records {
		users = append(users, record.ID)
		passwords = append(passwords, record.Value.Password)
	}
	prepared, err := b.service.PrepareUsers(users, passwords)
	if err != nil {
		return nil, E.Cause(err, "prepare Trojan authentication state")
	}
	return &publishedUsers{
		service:  b.service,
		prepared: prepared,
	}, nil
}

type publishedUsers struct {
	service  *trojanTransport.Service[adapter.UserID]
	prepared *trojanTransport.PreparedUsers[adapter.UserID]
}

func (p *publishedUsers) Commit() {
	p.service.InstallUsers(p.prepared)
}

func fingerprintTrojanString(fingerprint uint64, value string) uint64 {
	length := uint64(len(value))
	for range 8 {
		fingerprint = (fingerprint ^ uint64(byte(length))) * trojanFingerprintPrime64
		length >>= 8
	}
	for index := range len(value) {
		fingerprint = (fingerprint ^ uint64(value[index])) * trojanFingerprintPrime64
	}
	return fingerprint
}

func splitTrojanUsers(users []option.TrojanUser) ([]option.TrojanUser, []string) {
	managedUsers := make([]option.TrojanUser, 0, len(users))
	unmanagedPasswords := make([]string, 0, len(users))
	for _, user := range users {
		if user.Name == "" {
			unmanagedPasswords = append(unmanagedPasswords, user.Password)
			continue
		}
		managedUsers = append(managedUsers, user)
	}
	return managedUsers, unmanagedPasswords
}

func (h *Inbound) initializeUserManager(ctx context.Context, users []option.TrojanUser) error {
	managedUsers, unmanagedPasswords := splitTrojanUsers(users)
	manager := usermanager.New[option.TrojanUser](
		newUserBackend(h.service, unmanagedPasswords),
		usermanager.Options{},
	)
	if _, err := manager.ReplaceUsers(ctx, 0, "", "", managedUsers); err != nil {
		return E.Cause(err, "initialize Trojan managed users")
	}
	h.userManager = manager
	return nil
}

func (h *Inbound) Generation() adapter.UserGeneration {
	return h.userManager.Generation()
}

func (h *Inbound) ApplyUsers(
	ctx context.Context,
	transaction adapter.UserTransaction[option.TrojanUser],
) (adapter.UserTransactionResult, error) {
	return h.userManager.ApplyUsers(ctx, transaction)
}

func (h *Inbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.TrojanUser,
) (adapter.UserTransactionResult, error) {
	return h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		users,
	)
}
