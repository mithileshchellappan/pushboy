package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2/google"
)

const googleServiceAccountTokenURL = "https://oauth2.googleapis.com/token"

var (
	encodingKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	appleIDPattern       = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	appleBundlePattern   = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$`)
	googleProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
)

type TenantProviderNamespace struct {
	Provider      string `json:"provider"`
	Environment   string `json:"environment"`
	ApplicationID string `json:"applicationId"`
}

func (n TenantProviderNamespace) valid() bool {
	switch n.Provider {
	case "apns":
		return (n.Environment == "sandbox" || n.Environment == "production") &&
			len(n.ApplicationID) <= 255 && appleBundlePattern.MatchString(n.ApplicationID)
	case "fcm":
		return n.Environment == "production" && googleProjectPattern.MatchString(n.ApplicationID)
	default:
		return false
	}
}

type APNsCredentials struct {
	KeyID      string `json:"keyId"`
	TeamID     string `json:"teamId"`
	PrivateKey string `json:"privateKey"`
}

type fcmCredentials struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
	Universe     string `json:"universe_domain,omitempty"`
}

func validateProviderCredentials(ctx context.Context, n TenantProviderNamespace, raw json.RawMessage) ([]byte, error) {
	if !n.valid() {
		return nil, ErrInvalidTenantResource
	}
	switch n.Provider {
	case "apns":
		var creds APNsCredentials
		if json.Unmarshal(raw, &creds) != nil || !appleIDPattern.MatchString(creds.KeyID) || !appleIDPattern.MatchString(creds.TeamID) {
			return nil, ErrInvalidTenantResource
		}
		key, err := jwt.ParseECPrivateKeyFromPEM([]byte(creds.PrivateKey))
		if err != nil || key.Curve != elliptic.P256() {
			return nil, ErrInvalidTenantResource
		}
		return json.Marshal(creds)
	case "fcm":
		var creds fcmCredentials
		if json.Unmarshal(raw, &creds) != nil || creds.Type != "service_account" || !googleProjectPattern.MatchString(creds.ProjectID) ||
			creds.TokenURI != googleServiceAccountTokenURL || (creds.Universe != "" && creds.Universe != "googleapis.com") ||
			!validTenantResourceText(creds.PrivateKeyID, 128) {
			return nil, ErrInvalidTenantResource
		}
		address, err := mail.ParseAddress(creds.ClientEmail)
		if err != nil || address.Address != creds.ClientEmail || !strings.HasSuffix(creds.ClientEmail, ".gserviceaccount.com") {
			return nil, ErrInvalidTenantResource
		}
		key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(creds.PrivateKey))
		if err != nil || key.N.BitLen() < 2048 {
			return nil, ErrInvalidTenantResource
		}
		normalized, err := json.Marshal(creds)
		if err != nil {
			return nil, err
		}
		if _, err := google.CredentialsFromJSON(ctx, normalized, "https://www.googleapis.com/auth/firebase.messaging"); err != nil {
			return nil, ErrInvalidTenantResource
		}
		return normalized, nil
	default:
		return nil, ErrInvalidTenantResource
	}
}

type providerEncryptionKeys struct {
	active string
	keys   map[string]cipher.AEAD
}

func parseProviderEncryptionKeys(activeID, keysJSON string) (providerEncryptionKeys, error) {
	keys := providerEncryptionKeys{active: activeID, keys: make(map[string]cipher.AEAD)}
	var diagnostic error
	if activeID != "" && !encodingKeyIDPattern.MatchString(activeID) {
		keys.active = ""
		diagnostic = errors.New("invalid tenant provider active encryption key ID")
	}
	if keysJSON == "" {
		return keys, diagnostic
	}
	var encoded map[string]json.RawMessage
	if json.Unmarshal([]byte(keysJSON), &encoded) != nil || encoded == nil {
		return keys, errors.Join(diagnostic, errors.New("invalid tenant provider encryption key map"))
	}
	badEntries := 0
	for id, raw := range encoded {
		if !encodingKeyIDPattern.MatchString(id) {
			badEntries++
			continue
		}
		key, err := parseProviderEncryptionKey(raw)
		if err != nil {
			badEntries++
			continue
		}
		keys.keys[id] = key
	}
	if badEntries != 0 {
		diagnostic = errors.Join(diagnostic, fmt.Errorf("ignored %d invalid tenant provider encryption key entries", badEntries))
	}
	return keys, diagnostic
}

func parseProviderEncryptionKey(raw json.RawMessage) (cipher.AEAD, error) {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("provider encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type encryptedProviderCredentials struct {
	keyID      string
	nonce      []byte
	ciphertext []byte
}

func providerCredentialContext(tenantID uuid.UUID, route TenantProvider, keyID string) []byte {
	return fmt.Appendf(nil, "pushboy-provider-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s",
		tenantID, route.ID, route.Provider, route.Environment, route.ApplicationID, route.CredentialVersion, keyID)
}

func (keys providerEncryptionKeys) encrypt(tenantID uuid.UUID, route TenantProvider, plaintext []byte) (encryptedProviderCredentials, error) {
	key := keys.keys[keys.active]
	if key == nil {
		return encryptedProviderCredentials{}, ErrProviderEncryptionUnavailable
	}
	nonce := make([]byte, key.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return encryptedProviderCredentials{}, err
	}
	aad := providerCredentialContext(tenantID, route, keys.active)
	return encryptedProviderCredentials{keys.active, nonce, key.Seal(nil, nonce, plaintext, aad)}, nil
}

func (keys providerEncryptionKeys) open(ctx context.Context, tenantID uuid.UUID, route TenantProvider, credential encryptedProviderCredentials) ([]byte, string) {
	key := keys.keys[credential.keyID]
	if key == nil {
		return nil, "encryption_key_unavailable"
	}
	if len(credential.nonce) != key.NonceSize() {
		return nil, "credential_unreadable"
	}
	aad := providerCredentialContext(tenantID, route, credential.keyID)
	plaintext, err := key.Open(nil, credential.nonce, credential.ciphertext, aad)
	if err != nil {
		return nil, "credential_unreadable"
	}
	validated, err := validateProviderCredentials(ctx, route.TenantProviderNamespace, plaintext)
	if err != nil {
		return nil, "credential_invalid"
	}
	return validated, ""
}
