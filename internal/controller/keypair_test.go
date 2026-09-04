package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestGenerateECDSAKeyPair(t *testing.T) {
	pubPEM, privPEM, err := generateECDSAKeyPair()
	if err != nil {
		t.Fatalf("generateECDSAKeyPair() error: %v", err)
	}

	// validate public key PEM
	pubBlock, _ := pem.Decode(pubPEM)
	if pubBlock == nil {
		t.Fatal("failed to decode public key PEM")
		return
	}
	if pubBlock.Type != "PUBLIC KEY" {
		t.Errorf("public key PEM type = %q, want %q", pubBlock.Type, "PUBLIC KEY")
	}
	pubKey, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse public key: %v", err)
	}
	ecPub, ok := pubKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("public key is not ECDSA")
	}
	if ecPub.Curve != elliptic.P256() {
		t.Error("public key curve is not P-256")
	}

	// validate private key PEM
	privBlock, _ := pem.Decode(privPEM)
	if privBlock == nil {
		t.Fatal("failed to decode private key PEM")
		return
	}
	if privBlock.Type != "EC PRIVATE KEY" {
		t.Errorf("private key PEM type = %q, want %q", privBlock.Type, "EC PRIVATE KEY")
	}
	privKey, err := x509.ParseECPrivateKey(privBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse private key: %v", err)
	}
	if privKey.Curve != elliptic.P256() {
		t.Error("private key curve is not P-256")
	}
}

func TestGenerateECDSAKeyPair_Unique(t *testing.T) {
	pub1, _, err := generateECDSAKeyPair()
	if err != nil {
		t.Fatalf("first generateECDSAKeyPair() error: %v", err)
	}
	pub2, _, err := generateECDSAKeyPair()
	if err != nil {
		t.Fatalf("second generateECDSAKeyPair() error: %v", err)
	}
	if string(pub1) == string(pub2) {
		t.Error("two calls should produce different keys")
	}
}
