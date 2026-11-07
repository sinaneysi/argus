// Package brand holds the product name (ADR-0015 D2). It is the one place the UI, the TOTP issuer and the
// sign-in pages read it from. Not configurable: an adopter keeps the product name and sets COMPANY_NAME.
// Frozen identifiers that still say "argus" (the TOTP AES-GCM associated data in auth/secretbox.go, labels and
// markers stored in Jira, GitLab and Outline) are wire constants, not the brand; see ADR-0015 D3 and D4.
package brand

// Product is the product name shown to people.
const Product = "Argus"
