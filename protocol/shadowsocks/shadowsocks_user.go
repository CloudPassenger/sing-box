package shadowsocks

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

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
	shadowsocksSSMUserIDPrefix            = "\x00sing-box:shadowsocks:ssm:"
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

type shadowsocksUserState struct {
	access  sync.Mutex
	managed map[adapter.UserID]option.ShadowsocksUser
	ssm     map[string]string
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
	h.userState = newShadowsocksUserState(managedUsers)
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
	if err := validateManagedShadowsocksTransaction(transaction); err != nil {
		return adapter.UserTransactionResult{}, err
	}
	h.userState.access.Lock()
	defer h.userState.access.Unlock()

	result, err := h.userManager.ApplyUsers(ctx, transaction)
	if err != nil {
		return adapter.UserTransactionResult{}, err
	}
	if !result.Replayed {
		for _, operation := range transaction.Operations {
			switch operation.Type {
			case adapter.UserOperationAdd, adapter.UserOperationUpdate:
				h.userState.managed[operation.ID] = operation.Value
			case adapter.UserOperationDelete:
				delete(h.userState.managed, operation.ID)
			}
		}
	}
	return externalShadowsocksResult(result), nil
}

func (h *MultiInbound) ReplaceUsers(
	ctx context.Context,
	expectedGeneration adapter.UserGeneration,
	requestID string,
	sourceRevision string,
	users []option.ShadowsocksUser,
) (adapter.UserTransactionResult, error) {
	for _, user := range users {
		if err := validateManagedShadowsocksUserID(adapter.UserID(user.Name)); err != nil {
			return adapter.UserTransactionResult{}, err
		}
	}
	h.userState.access.Lock()
	defer h.userState.access.Unlock()

	canonicalUsers := appendShadowsocksSSMUsers(slices.Clone(users), h.userState.ssm)
	result, err := h.userManager.ReplaceUsers(
		ctx,
		expectedGeneration,
		requestID,
		sourceRevision,
		canonicalUsers,
	)
	if err != nil {
		return adapter.UserTransactionResult{}, err
	}
	if !result.Replayed {
		h.userState.managed = shadowsocksUsersByID(users)
	}
	return externalShadowsocksResult(result), nil
}

func newShadowsocksUserState(users []option.ShadowsocksUser) *shadowsocksUserState {
	return &shadowsocksUserState{
		managed: shadowsocksUsersByID(users),
		ssm:     make(map[string]string),
	}
}

func shadowsocksUsersByID(users []option.ShadowsocksUser) map[adapter.UserID]option.ShadowsocksUser {
	usersByID := make(map[adapter.UserID]option.ShadowsocksUser, len(users))
	for _, user := range users {
		usersByID[adapter.UserID(user.Name)] = user
	}
	return usersByID
}

func validateManagedShadowsocksTransaction(
	transaction adapter.UserTransaction[option.ShadowsocksUser],
) error {
	for _, operation := range transaction.Operations {
		if err := validateManagedShadowsocksUserID(operation.ID); err != nil {
			return err
		}
		switch operation.Type {
		case adapter.UserOperationAdd, adapter.UserOperationUpdate:
			if err := validateManagedShadowsocksUserID(adapter.UserID(operation.Value.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateManagedShadowsocksUserID(userID adapter.UserID) error {
	if strings.HasPrefix(string(userID), shadowsocksSSMUserIDPrefix) {
		return E.Cause(
			usermanager.ErrInvalidTransaction,
			"Shadowsocks user ID uses the reserved SSM namespace",
		)
	}
	return nil
}

func (h *MultiInbound) replaceSSMUsers(users []string, passwords []string) error {
	if len(users) != len(passwords) {
		return E.New("Shadowsocks SSM user and password list length mismatch")
	}
	desired := make(map[string]string, len(users))
	for index, user := range users {
		if user == "" {
			return E.New("empty Shadowsocks SSM user name")
		}
		if _, loaded := desired[user]; loaded {
			return E.New("duplicate Shadowsocks SSM user name")
		}
		desired[user] = passwords[index]
	}

	h.userState.access.Lock()
	defer h.userState.access.Unlock()
	canonicalUsers := make([]option.ShadowsocksUser, 0, len(h.userState.managed)+len(desired))
	for _, user := range h.userState.managed {
		canonicalUsers = append(canonicalUsers, user)
	}
	canonicalUsers = appendShadowsocksSSMUsers(canonicalUsers, desired)
	if _, err := h.userManager.ReplaceUsers(h.ctx, 0, "", "", canonicalUsers); err != nil {
		return err
	}
	h.userState.ssm = desired
	return nil
}

func appendShadowsocksSSMUsers(
	users []option.ShadowsocksUser,
	ssmUsers map[string]string,
) []option.ShadowsocksUser {
	for user, password := range ssmUsers {
		users = append(users, option.ShadowsocksUser{
			Name:     shadowsocksSSMUserIDPrefix + user,
			Password: password,
		})
	}
	return users
}

func externalShadowsocksResult(result adapter.UserTransactionResult) adapter.UserTransactionResult {
	result.Added = externalShadowsocksUserIDs(result.Added)
	result.Updated = externalShadowsocksUserIDs(result.Updated)
	result.Deleted = externalShadowsocksUserIDs(result.Deleted)
	return result
}

func externalShadowsocksUserIDs(userIDs []adapter.UserID) []adapter.UserID {
	return slices.DeleteFunc(slices.Clone(userIDs), func(userID adapter.UserID) bool {
		return strings.HasPrefix(string(userID), shadowsocksSSMUserIDPrefix)
	})
}

func (h *MultiInbound) resolveUserID(userID adapter.UserID) (string, bool) {
	if unmanagedLabel, unmanaged := h.unmanagedUserLabels[userID]; unmanaged {
		return unmanagedLabel, false
	}
	if user, ssmUser := strings.CutPrefix(string(userID), shadowsocksSSMUserIDPrefix); ssmUser {
		return user, true
	}
	return string(userID), true
}
