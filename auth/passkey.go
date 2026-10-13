package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"tvt/logger"
	"tvt/store"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

var (
	ErrPasskeyUnavailable = errors.New("passkeys are not configured")
	ErrPasskeyFailed      = errors.New("passkey verification failed")
	ErrChallengeExpired   = errors.New("passkey challenge expired")
)

type challenge struct {
	session webauthn.SessionData
	userID  int64
	expires time.Time
}

type passkeyState struct {
	wa         *webauthn.WebAuthn
	mu         sync.Mutex
	challenges map[string]challenge
}

type waUser struct {
	u      *store.User
	handle []byte
	creds  []webauthn.Credential
}

func (w *waUser) WebAuthnID() []byte                         { return w.handle }
func (w *waUser) WebAuthnName() string                       { return w.u.Username }
func (w *waUser) WebAuthnDisplayName() string                { return w.u.Name() }
func (w *waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

// EnableWebAuthn turns on passkeys. rpID is the registrable host passkeys are
// bound to and origin is the exact site origin browsers will report.
func (s *Service) EnableWebAuthn(rpID, origin, displayName string) error {
	wa, err := webauthn.New(&webauthn.Config{RPDisplayName: displayName, RPID: rpID, RPOrigins: []string{origin}})
	if err != nil {
		return fmt.Errorf("webauthn config: %w", err)
	}
	s.pk = &passkeyState{wa: wa, challenges: map[string]challenge{}}
	return nil
}

func (s *Service) PasskeysEnabled() bool { return s.pk != nil }

func (s *Service) loadWAUser(u *store.User) (*waUser, error) {
	handle, err := s.Store.WebAuthnHandle(u.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.Passkeys(u.ID)
	if err != nil {
		return nil, err
	}
	out := &waUser{u: u, handle: handle}
	for _, r := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(r.Credential), &c); err != nil {
			logger.Error("PASSKEY decode credential id=%d uid=%d: %v", r.ID, u.ID, err)
			continue
		}
		out.creds = append(out.creds, c)
	}
	return out, nil
}

func (s *Service) putChallenge(w http.ResponseWriter, sd webauthn.SessionData, userID int64) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("challenge id entropy: %w", err)
	}
	id := hex.EncodeToString(b)
	now := time.Now()
	s.pk.mu.Lock()
	for k, c := range s.pk.challenges {
		if now.After(c.expires) {
			delete(s.pk.challenges, k)
		}
	}
	s.pk.challenges[id] = challenge{session: sd, userID: userID, expires: now.Add(5 * time.Minute)}
	s.pk.mu.Unlock()
	s.setField(w, fieldWebAuthn, id)
	return nil
}

func (s *Service) takeChallenge(r *http.Request) (challenge, error) {
	id := s.field(r, fieldWebAuthn)
	if id == "" {
		return challenge{}, ErrChallengeExpired
	}
	s.pk.mu.Lock()
	defer s.pk.mu.Unlock()
	ch, ok := s.pk.challenges[id]
	delete(s.pk.challenges, id)
	if !ok || time.Now().After(ch.expires) {
		return challenge{}, ErrChallengeExpired
	}
	return ch, nil
}

func (s *Service) BeginPasskeyLogin(w http.ResponseWriter) (*protocol.CredentialAssertion, error) {
	if s.pk == nil {
		return nil, ErrPasskeyUnavailable
	}
	assertion, sd, err := s.pk.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("begin passkey login: %w", err)
	}
	if err := s.putChallenge(w, *sd, 0); err != nil {
		return nil, err
	}
	return assertion, nil
}

// FinishPasskeyLogin verifies a discoverable-credential assertion. A passkey
// replaces the password as the first factor; an account with two-step
// verification still has to pass its TOTP step afterwards.
func (s *Service) FinishPasskeyLogin(r *http.Request) (*store.User, error) {
	if s.pk == nil {
		return nil, ErrPasskeyUnavailable
	}
	ch, err := s.takeChallenge(r)
	if err != nil {
		return nil, err
	}
	var found *waUser
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := s.Store.UserByWebAuthnHandle(userHandle)
		if err != nil {
			return nil, err
		}
		wu, err := s.loadWAUser(u)
		if err != nil {
			return nil, err
		}
		found = wu
		return wu, nil
	}
	_, cred, err := s.pk.wa.FinishPasskeyLogin(handler, ch.session, r)
	if err != nil || found == nil {
		logger.Warn("PASSKEY login failed from=%s: %v", ClientCountry(r), err)
		return nil, ErrPasskeyFailed
	}
	if cred.Authenticator.CloneWarning {
		logger.Error("PASSKEY clone warning uid=%d from=%s", found.u.ID, ClientCountry(r))
		return nil, ErrPasskeyFailed
	}
	if found.u.IsBanned {
		return nil, ErrBanned
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, fmt.Errorf("encode passkey uid=%d: %w", found.u.ID, err)
	}
	if err := s.Store.UpdatePasskeyUse(found.u.ID, cred.ID, string(raw)); err != nil {
		return nil, err
	}
	return found.u, nil
}

func (s *Service) BeginPasskeyRegistration(w http.ResponseWriter, u *store.User) (*protocol.CredentialCreation, error) {
	if s.pk == nil {
		return nil, ErrPasskeyUnavailable
	}
	wu, err := s.loadWAUser(u)
	if err != nil {
		return nil, err
	}
	excl := make([]protocol.CredentialDescriptor, 0, len(wu.creds))
	for i := range wu.creds {
		excl = append(excl, wu.creds[i].Descriptor())
	}
	rk := true
	creation, sd, err := s.pk.wa.BeginRegistration(wu,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: &rk,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(excl))
	if err != nil {
		return nil, fmt.Errorf("begin passkey registration uid=%d: %w", u.ID, err)
	}
	if err := s.putChallenge(w, *sd, u.ID); err != nil {
		return nil, err
	}
	return creation, nil
}

func (s *Service) FinishPasskeyRegistration(r *http.Request, u *store.User, name string) error {
	if s.pk == nil {
		return ErrPasskeyUnavailable
	}
	ch, err := s.takeChallenge(r)
	if err != nil {
		return err
	}
	if ch.userID != u.ID {
		return ErrChallengeExpired
	}
	wu, err := s.loadWAUser(u)
	if err != nil {
		return err
	}
	cred, err := s.pk.wa.FinishRegistration(wu, ch.session, r)
	if err != nil {
		logger.Warn("PASSKEY registration failed uid=%d: %v", u.ID, err)
		return ErrPasskeyFailed
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("encode passkey uid=%d: %w", u.ID, err)
	}
	return s.Store.AddPasskey(u.ID, cred.ID, string(raw), name)
}
