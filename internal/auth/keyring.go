package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// KeyringService is the single service name under which every Posthaste secret
// is stored in the OS keyring. Account entries are distinguished by the
// CredentialRef used as the keyring user key.
const KeyringService = "posthaste"

// ErrCredentialNotFound is returned when the keyring holds no secret for the
// requested reference. The underlying keyring error is wrapped alongside it,
// so errors.Is matches both this sentinel and keyring.ErrNotFound.
var ErrCredentialNotFound = errors.New("auth: credential not found in keyring")

// keyringBackend is the narrow slice of the OS keyring this package needs. It
// exists so reference handling and error mapping can be tested on machines
// without a Secret Service; production wires it to zalando/go-keyring.
type keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// systemKeyring adapts zalando/go-keyring's package-level functions to the
// keyringBackend interface. This adapter is the only place the library is
// imported, keeping the rest of the package testable without a keyring.
type systemKeyring struct{}

// Get reads the secret for service and user from the OS credential store.
func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

// Set stores a secret for service and user in the OS credential store.
func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

// Delete removes the secret for service and user from the OS credential store.
func (systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// Keyring stores and retrieves account secrets in the OS credential store.
// A zero value is not usable; construct one with NewKeyring.
type Keyring struct {
	backend keyringBackend
}

// NewKeyring returns a Keyring backed by the real OS credential store (Secret
// Service via GNOME Keyring or KWallet on Linux).
func NewKeyring() *Keyring {
	return &Keyring{backend: systemKeyring{}}
}

// newKeyringWithBackend wires an explicit backend so tests can substitute a
// fake and stay hermetic.
func newKeyringWithBackend(backend keyringBackend) *Keyring {
	return &Keyring{backend: backend}
}

// CredentialRef returns the deterministic keyring reference for an account.
// The reference is safe to store in SQLite: it names the keyring entry without
// containing any secret material.
func CredentialRef(accountID string) string {
	return "account/" + accountID
}

// SaveCredential stores secret under ref in the OS keyring. The context is
// honoured before the call starts; the underlying credential-store call is
// synchronous and not interruptible.
func (k *Keyring) SaveCredential(ctx context.Context, ref, secret string) error {
	if ref == "" {
		return errors.New("auth: save credential: empty reference")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("auth: save credential: %w", err)
	}
	if err := k.backend.Set(KeyringService, ref, secret); err != nil {
		return fmt.Errorf("auth: save credential: %w", err)
	}
	return nil
}

// Credential loads the secret stored under ref, or an error wrapping
// ErrCredentialNotFound when the keyring has no such entry.
func (k *Keyring) Credential(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("auth: load credential: empty reference")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("auth: load credential: %w", err)
	}
	secret, err := k.backend.Get(KeyringService, ref)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			// Wrapping both sentinels keeps either match working for callers.
			return "", fmt.Errorf("auth: load credential: %w: %w", ErrCredentialNotFound, err)
		}
		return "", fmt.Errorf("auth: load credential: %w", err)
	}
	return secret, nil
}

// DeleteCredential removes the secret stored under ref. Deleting an entry that
// does not exist succeeds anyway, which keeps account removal idempotent.
func (k *Keyring) DeleteCredential(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("auth: delete credential: empty reference")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("auth: delete credential: %w", err)
	}
	if err := k.backend.Delete(KeyringService, ref); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("auth: delete credential: %w", err)
	}
	return nil
}
