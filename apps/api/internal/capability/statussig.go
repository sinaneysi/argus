package capability

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
)

// statusDomain separates a status signature from every other signature of these keys.
const statusDomain = "argus-actd-status-v1\n"

// SignStatus signs the bytes of actd's status (ADR-0022 PR 5 review): the api trusts the heartbeat only with this
// signature, because the worker can write the shared volume.
func SignStatus(k ed25519.PrivateKey, body []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k, append([]byte(statusDomain), body...)))
}

// VerifyStatus checks a status signature with the pinned public key.
func VerifyStatus(pub ed25519.PublicKey, body []byte, sig string) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("no status key of the executor is pinned")
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || sig == "" {
		return errors.New("the status is not signed")
	}
	if !ed25519.Verify(pub, append([]byte(statusDomain), body...), raw) {
		return errors.New("the status signature does not verify")
	}
	return nil
}
