package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

const hostKeyFile = "host_ed25519"

// LoadOrCreateHostKey returns the server's host key, generating an ed25519 key into
// dir on first boot (PRD §9: secrets are generated into data/, never shipped).
func LoadOrCreateHostKey(dir string) (ssh.Signer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, hostKeyFile)
	raw, err := os.ReadFile(p) // #nosec G304 -- path is <data>/ssh/host_ed25519
	switch {
	case err == nil:
		return ssh.ParsePrivateKey(raw)
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "codr host key")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p+".pub", ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		return nil, err
	}
	return signer, nil
}
