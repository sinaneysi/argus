package capability

// approve.go: signed approvals (ADR-0022 amendment 1, item A). The api signs every approval of a plan, every
// "continue after the operator step" and every manual rollback with an Ed25519 key that only the api holds. actd
// holds only the public key, in its own volume, and refuses a step without a valid signature. The worker passes
// the signature on but cannot make one, so a forged approved_* row in board.db does not execute anything.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The kinds of signed approval.
const (
	SignStart    = "start"    // Spin up agent: the plan version and hash, run the capability steps
	SignContinue = "continue" // the operator did the operator step Step: run the steps after it
	SignRollback = "rollback" // roll back step Step of the action
)

// approvalDomain separates these signatures from any other use of the key.
const approvalDomain = "argus-approval-v1"

// ApprovalClaim is what one signature covers.
type ApprovalClaim struct {
	Kind         string `json:"kind"`
	TaskKey      string `json:"task_key"`
	PlanVersion  int    `json:"plan_version"`
	PlanSHA256   string `json:"plan_sha256"`
	Approver     string `json:"approver"`
	At           int64  `json:"at"` // unix seconds, the api's clock
	SealedSHA256 string `json:"sealed_sha256"`
	Step         int    `json:"step,omitempty"` // continue: the operator step done; rollback: the step to roll back
}

var (
	hex64Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	claimKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	claimUser  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)
)

// Message is the exact byte string that is signed: one field per line, fixed order, no field may hold a newline.
func (c ApprovalClaim) Message() ([]byte, error) {
	switch {
	case c.Kind != SignStart && c.Kind != SignContinue && c.Kind != SignRollback:
		return nil, errors.New("unknown approval kind")
	case !claimKeyRe.MatchString(c.TaskKey):
		return nil, errors.New("bad task key")
	case c.PlanVersion <= 0:
		return nil, errors.New("bad plan version")
	case !hex64Re.MatchString(c.PlanSHA256) || !hex64Re.MatchString(c.SealedSHA256):
		return nil, errors.New("bad hash")
	case !claimUser.MatchString(c.Approver):
		return nil, errors.New("bad approver")
	case c.At <= 0:
		return nil, errors.New("bad approval time")
	case c.Step < 0 || (c.Kind == SignStart) != (c.Step == 0):
		return nil, errors.New("bad step")
	}
	return []byte(strings.Join([]string{approvalDomain, c.Kind, c.TaskKey, strconv.Itoa(c.PlanVersion), c.PlanSHA256, c.Approver,
		strconv.FormatInt(c.At, 10), c.SealedSHA256, strconv.Itoa(c.Step)}, "\n")), nil
}

// SignApproval signs the claim; the signature is standard base64.
func SignApproval(key ed25519.PrivateKey, c ApprovalClaim) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("no approval signing key")
	}
	m, err := c.Message()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, m)), nil
}

// VerifyApproval checks the signature of the claim against the public key.
func VerifyApproval(pub ed25519.PublicKey, c ApprovalClaim, sig string) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("no approval public key in the executor volume (argus actd approval-key set)")
	}
	if sig == "" {
		return errors.New("the approval is not signed")
	}
	m, err := c.Message()
	if err != nil {
		return fmt.Errorf("the signed approval is not usable: %v", err)
	}
	s, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(s) != ed25519.SignatureSize || !ed25519.Verify(pub, m, s) {
		return errors.New("the approval signature is not valid")
	}
	return nil
}

// SealedSHA256 is the hash of the sealed inputs (name -> ciphertext) an approval binds to: SHA-256 of their
// canonical JSON (sorted keys); no inputs hash as {}.
func SealedSHA256(in map[string]string) string {
	if in == nil {
		in = map[string]string{}
	}
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NewApprovalKey is a new Ed25519 key pair as PEM (PKCS#8 private key, PKIX public key).
func NewApprovalKey() (priv, pub []byte, err error) {
	p, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	pubPEM, err := PublicKeyPEM(p)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), pubPEM, nil
}

// PublicKeyPEM is the PKIX PEM of an Ed25519 public key.
func PublicKeyPEM(p ed25519.PublicKey) ([]byte, error) {
	b, err := x509.MarshalPKIXPublicKey(p)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), nil
}

// ParseApprovalPrivate reads a PKCS#8 PEM Ed25519 private key (as made by NewApprovalKey or
// `openssl genpkey -algorithm ed25519`).
func ParseApprovalPrivate(b []byte) (ed25519.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errors.New("the approval key is not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the approval key: %v", err)
	}
	ek, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("the approval key is not an Ed25519 key")
	}
	return ek, nil
}

// ParseApprovalPublic reads a PKIX PEM Ed25519 public key.
func ParseApprovalPublic(b []byte) (ed25519.PublicKey, error) {
	blk, rest := pem.Decode(b)
	if blk == nil || blk.Type != "PUBLIC KEY" || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("the approval public key is not one PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the approval public key: %v", err)
	}
	ek, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("the approval public key is not an Ed25519 key")
	}
	return ek, nil
}

// KeyFingerprint is the first 16 hex characters of SHA-256 of the public key, to compare the two sides.
func KeyFingerprint(p ed25519.PublicKey) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:8])
}
