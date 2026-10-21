// overclaud-release is the maintainer tool that signs a release. Not shipped to customers.
//
//	overclaud-release keygen            create the signing key (once) and print the public key
//	overclaud-release pubkey            print the public key
//	overclaud-release sign <root> <ver> write sync/manifest.json and sync/manifest.sig in root
//	overclaud-release verify <root>     check a signed tree with the public key
//
// The private key lives at ~/.config/overclaud/release-signing.key (0600). Back it up
// offline: without it no installer will accept a new release.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/psumo/overclaud-installer/internal/manifest"
	"github.com/psumo/overclaud-installer/internal/payload"
)

func keyPath() string {
	if p := os.Getenv("OVERCLAUD_SIGNING_KEY"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "overclaud", "release-signing.key")
}

func loadKey() (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(keyPath())
	if err != nil {
		return nil, fmt.Errorf("no signing key at %s (run keygen)", keyPath())
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("signing key file is corrupt")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: overclaud-release keygen | pubkey | sign <root> <version> | verify <root>")
	}
	switch args[0] {
	case "keygen":
		if _, err := os.Stat(keyPath()); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite a signing key", keyPath())
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(keyPath()), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(keyPath(), []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println(hex.EncodeToString(pub))
		return nil
	case "pubkey":
		priv, err := loadKey()
		if err != nil {
			return err
		}
		fmt.Println(hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return nil
	case "sign":
		if len(args) != 3 {
			return errors.New("usage: sign <root> <version>")
		}
		priv, err := loadKey()
		if err != nil {
			return err
		}
		m, err := manifest.Build(args[1], args[2])
		if err != nil {
			return err
		}
		data, err := m.Marshal()
		if err != nil {
			return err
		}
		dir := filepath.Join(args[1], "sync")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(args[1], filepath.FromSlash(manifest.FileName)), data, 0o644); err != nil {
			return err
		}
		sig := hex.EncodeToString(manifest.Sign(data, priv)) + "\n"
		if err := os.WriteFile(filepath.Join(args[1], filepath.FromSlash(manifest.SigName)), []byte(sig), 0o644); err != nil {
			return err
		}
		fmt.Printf("signed %d files, version %s\n", len(m.Files), m.Version)
		return nil
	case "verify":
		if len(args) != 2 {
			return errors.New("usage: verify <root>")
		}
		priv, err := loadKey()
		if err != nil {
			return err
		}
		m, err := payload.Verify(args[1], priv.Public().(ed25519.PublicKey))
		if err != nil {
			return err
		}
		fmt.Printf("ok: %d files, version %s\n", len(m.Files), m.Version)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
