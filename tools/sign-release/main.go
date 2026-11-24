// sign-release signs a Gate Bridge release's SHA256SUMS so agents will
// install it (internal/update checks the signature against the public key
// compiled into them from release-signing.pub).
//
//	go run ./tools/sign-release -key <private key> -version 0.6.0 dist/SHA256SUMS
//	    writes dist/SHA256SUMS.sig (refuses when the key doesn't match
//	    release-signing.pub, or when there is no release-signing.pub)
//	go run ./tools/sign-release verify -version 0.6.0 dist/SHA256SUMS
//	    checks dist/SHA256SUMS.sig against release-signing.pub
//	go run ./tools/sign-release pubkey -key <private key>
//	    prints the hex public key for a private key
//	go run ./tools/sign-release keygen -out <folder outside the repository>
//	    makes a new key pair: <folder>/release-signing.key (KEEP SECRET, never
//	    commit it) and <folder>/release-signing.pub (commit this one)
//
// A private key file is either 64 hex characters (the Ed25519 seed, what
// keygen writes) or an OpenSSL PKCS#8 PEM file
// (`openssl genpkey -algorithm ed25519 -out release-signing.key`).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sign-release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "keygen":
			return keygen(args[1:])
		case "pubkey":
			return pubkey(args[1:])
		case "verify":
			return verify(args[1:])
		}
	}
	return sign(args)
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign-release", flag.ContinueOnError)
	keyPath := fs.String("key", "", "private key file")
	version := fs.String("version", "", "the release version, e.g. 0.6.0")
	pubPath := fs.String("pub", "release-signing.pub", "the committed public key the agents are built with")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || *version == "" || fs.NArg() != 1 {
		return errors.New("usage: sign-release -key <private key> -version <x.y.z> [-pub release-signing.pub] dist/SHA256SUMS")
	}
	priv, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	committed, err := os.ReadFile(*pubPath)
	if err != nil {
		return fmt.Errorf("%s isn't there (%v): commit the public key first, so the agents this release builds carry it", *pubPath, err)
	}
	pub, err := update.ParsePublicKey(string(committed))
	if err != nil {
		return fmt.Errorf("%s: %v", *pubPath, err)
	}
	if !pub.Equal(priv.Public()) {
		return fmt.Errorf("the private key doesn't match %s: the agents built from it would refuse this release", *pubPath)
	}
	sumsPath := fs.Arg(0)
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, update.SignedMessage(*version, sums))
	if err := update.VerifySums(pub, *version, sums, []byte(update.EncodeSignature(sig))); err != nil {
		return fmt.Errorf("the signature didn't verify (%v)", err)
	}
	out := sumsPath + ".sig"
	if err := os.WriteFile(out, []byte(update.EncodeSignature(sig)), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s for version %s -> %s\n", sumsPath, strings.TrimPrefix(*version, "v"), out)
	return nil
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	version := fs.String("version", "", "the release version")
	pubPath := fs.String("pub", "release-signing.pub", "public key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || fs.NArg() != 1 {
		return errors.New("usage: sign-release verify -version <x.y.z> [-pub release-signing.pub] dist/SHA256SUMS")
	}
	b, err := os.ReadFile(*pubPath)
	if err != nil {
		return err
	}
	pub, err := update.ParsePublicKey(string(b))
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(fs.Arg(0) + ".sig")
	if err != nil {
		return err
	}
	if err := update.VerifySums(pub, *version, sums, sig); err != nil {
		return err
	}
	fmt.Println("signature OK")
	return nil
}

func pubkey(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	keyPath := fs.String("key", "", "private key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	priv, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "folder to write the key pair into (keep it OUTSIDE the repository)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("usage: sign-release keygen -out <folder outside the repository>")
	}
	keyFile := filepath.Join(*out, "release-signing.key")
	pubFile := filepath.Join(*out, "release-signing.pub")
	for _, f := range []string{keyFile, pubFile} {
		if _, err := os.Stat(f); err == nil {
			return fmt.Errorf("%s already exists - refusing to replace a key", f)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pubFile, []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (SECRET - store it in a password manager and as the RELEASE_SIGNING_KEY secret; never commit it)\n", keyFile)
	fmt.Printf("wrote %s (copy it to the repository root and commit it)\n", pubFile)
	return nil
}

// readPrivateKey reads a hex seed or an OpenSSL PKCS#8 PEM Ed25519 key.
func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("no -key given")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parsePrivateKey(b)
}

func parsePrivateKey(b []byte) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode(b); block != nil {
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the PEM key isn't PKCS#8: %v", err)
		}
		priv, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("the PEM key isn't an Ed25519 key")
		}
		return priv, nil
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, errors.New("the key file is neither hex nor PEM")
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	}
	return nil, fmt.Errorf("the hex key is %d bytes, want a 32-byte seed", len(raw))
}
