package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeKeyring records backend calls so tests can assert exactly what the
// wrapper sent to the OS credential store, and can inject failures.
type fakeKeyring struct {
	secrets     map[string]string
	getErr      error
	setErr      error
	delErr      error
	calls       []string
	lastService string
	lastUser    string
}

func (f *fakeKeyring) key(service, user string) string {
	return service + "\x00" + user
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.calls = append(f.calls, "get")
	f.lastService, f.lastUser = service, user
	if f.getErr != nil {
		return "", f.getErr
	}
	secret, ok := f.secrets[f.key(service, user)]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return secret, nil
}

func (f *fakeKeyring) Set(service, user, password string) error {
	f.calls = append(f.calls, "set")
	f.lastService, f.lastUser = service, user
	if f.setErr != nil {
		return f.setErr
	}
	if f.secrets == nil {
		f.secrets = map[string]string{}
	}
	f.secrets[f.key(service, user)] = password
	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	f.calls = append(f.calls, "delete")
	f.lastService, f.lastUser = service, user
	if f.delErr != nil {
		return f.delErr
	}
	if _, ok := f.secrets[f.key(service, user)]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.secrets, f.key(service, user))
	return nil
}

func TestCredentialRef(t *testing.T) {
	tests := []struct {
		name      string
		accountID string
		want      string
	}{
		{name: "plain id", accountID: "a1b2c3", want: "account/a1b2c3"},
		{name: "uuid-shaped id", accountID: "8f0d2f1e-11ac-4f5e-9f2a-6f4f5cf1a123", want: "account/8f0d2f1e-11ac-4f5e-9f2a-6f4f5cf1a123"},
		{name: "empty id still forms a stable prefix", accountID: "", want: "account/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CredentialRef(tc.accountID)
			if got != tc.want {
				t.Fatalf("CredentialRef(%q) = %q, want %q", tc.accountID, got, tc.want)
			}
			if again := CredentialRef(tc.accountID); again != got {
				t.Fatalf("CredentialRef(%q) is not deterministic: %q then %q", tc.accountID, got, again)
			}
		})
	}
}

func TestKeyringRoundTrip(t *testing.T) {
	fake := &fakeKeyring{}
	kr := newKeyringWithBackend(fake)
	ctx := context.Background()
	ref := CredentialRef("acc-1")

	if err := kr.SaveCredential(ctx, ref, "app-password-1"); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}
	if fake.lastService != KeyringService || fake.lastUser != ref {
		t.Fatalf("backend called with service=%q user=%q, want service=%q user=%q",
			fake.lastService, fake.lastUser, KeyringService, ref)
	}

	secret, err := kr.Credential(ctx, ref)
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if secret != "app-password-1" {
		t.Fatalf("Credential = %q, want %q", secret, "app-password-1")
	}

	if err := kr.DeleteCredential(ctx, ref); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	_, loadErr := kr.Credential(ctx, ref)
	if !errors.Is(loadErr, ErrCredentialNotFound) {
		t.Fatalf("Credential after delete: err = %v, want ErrCredentialNotFound", loadErr)
	}
	if !errors.Is(loadErr, keyring.ErrNotFound) {
		t.Fatalf("Credential after delete: err = %v, keyring.ErrNotFound must still match", loadErr)
	}
}

func TestKeyringDeleteMissingSucceeds(t *testing.T) {
	kr := newKeyringWithBackend(&fakeKeyring{secrets: map[string]string{}})
	if err := kr.DeleteCredential(context.Background(), CredentialRef("gone")); err != nil {
		t.Fatalf("DeleteCredential of missing entry: %v, want nil", err)
	}
}

func TestKeyringMapsOnlyNotFoundErrors(t *testing.T) {
	backendErr := errors.New("dbus: connection closed by user")
	kr := newKeyringWithBackend(&fakeKeyring{getErr: backendErr})
	_, err := kr.Credential(context.Background(), CredentialRef("acc-1"))
	if err == nil {
		t.Fatal("Credential: want error on backend failure")
	}
	if errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Credential: unrelated backend error %v must not map to ErrCredentialNotFound", err)
	}
	if !errors.Is(err, backendErr) {
		t.Fatalf("Credential: error %v must wrap the backend error", err)
	}
}

func TestKeyringEmptyReferenceRejected(t *testing.T) {
	fake := &fakeKeyring{}
	kr := newKeyringWithBackend(fake)
	ctx := context.Background()
	if err := kr.SaveCredential(ctx, "", "secret"); err == nil {
		t.Fatal("SaveCredential with empty ref: want error")
	}
	if _, err := kr.Credential(ctx, ""); err == nil {
		t.Fatal("Credential with empty ref: want error")
	}
	if err := kr.DeleteCredential(ctx, ""); err == nil {
		t.Fatal("DeleteCredential with empty ref: want error")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("backend must not be called for empty refs, got calls %v", fake.calls)
	}
}

func TestKeyringHonoursCancelledContext(t *testing.T) {
	fake := &fakeKeyring{}
	kr := newKeyringWithBackend(fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := kr.SaveCredential(ctx, CredentialRef("acc-1"), "secret"); !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveCredential: err = %v, want context.Canceled", err)
	}
	if _, err := kr.Credential(ctx, CredentialRef("acc-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Credential: err = %v, want context.Canceled", err)
	}
	if err := kr.DeleteCredential(ctx, CredentialRef("acc-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteCredential: err = %v, want context.Canceled", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("backend must not be called after cancellation, got calls %v", fake.calls)
	}
}

func TestKeyringErrorContainsNoSecret(t *testing.T) {
	kr := newKeyringWithBackend(&fakeKeyring{setErr: errors.New("storage backend blew up")})
	err := kr.SaveCredential(context.Background(), CredentialRef("acc-1"), "super-secret-value")
	if err == nil {
		t.Fatal("SaveCredential: want error")
	}
	if got := err.Error(); strings.Contains(got, "super-secret-value") {
		t.Fatalf("error %q must not contain the secret", got)
	}
}
