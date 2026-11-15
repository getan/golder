package adb

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Identity is the persistent ADB client identity. The private key is PKCS#8;
// callers must keep the file in private app storage and wrap it with Android
// Keystore before production use.
type Identity struct {
	Private *rsa.PrivateKey
	Name    string
}

func GenerateIdentity(name string) (*Identity, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &Identity{Private: key, Name: sanitizeName(name)}, nil
}

func ParseIdentity(pkcs8 []byte, name string) (*Identity, error) {
	key, err := x509.ParsePKCS8PrivateKey(pkcs8)
	if err != nil {
		return nil, fmt.Errorf("parse adb PKCS#8 key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("adb key is %T, want RSA", key)
	}
	return &Identity{Private: rsaKey, Name: sanitizeName(name)}, nil
}

// LoadOrCreateIdentity reads path when present and never overwrites an
// unreadable key. Losing the pairing key silently would make the device reject
// every future connection.
func LoadOrCreateIdentity(path, name string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return ParseIdentity(data, name)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	identity, err := GenerateIdentity(name)
	if err != nil {
		return nil, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(identity.Private)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pkcs8, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return identity, nil
}

func (i *Identity) PKCS8() ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(i.Private)
}

// ADBPublicKey returns the line stored by adbd in
// /data/misc/adb/adb_keys: base64(524-byte RSAPublicKey) + name.
func (i *Identity) ADBPublicKey() string {
	return base64.StdEncoding.EncodeToString(EncodeRSAPublicKey(&i.Private.PublicKey)) + " " + i.Name
}

// EncodeRSAPublicKey implements adb's packed RSAPublicKey:
// uint32 len, uint32 n0inv, modulus[256], rr[256], uint32 exponent.
// The modulus and rr are big-endian; n0inv and exponent are little-endian.
func EncodeRSAPublicKey(pub *rsa.PublicKey) []byte {
	const size = 524
	out := make([]byte, size)
	n := new(big.Int).Set(pub.N)
	if n.BitLen() > 2048 {
		panic("ADB RSA key must be at most 2048 bits")
	}
	binary.LittleEndian.PutUint32(out[0:4], 256)
	n0inv := rsaN0Inv(n)
	binary.LittleEndian.PutUint32(out[4:8], n0inv)
	n.FillBytes(out[8:264])

	// rr = (2^2048)^2 mod n, used by adbd's Montgomery multiplication.
	r := new(big.Int).Lsh(big.NewInt(1), 2048)
	rr := new(big.Int).Exp(r, big.NewInt(2), n)
	rr.FillBytes(out[264:520])
	binary.LittleEndian.PutUint32(out[520:524], uint32(pub.E))
	return out
}

func rsaN0Inv(n *big.Int) uint32 {
	const mod = uint64(1) << 32
	n0 := new(big.Int).And(n, new(big.Int).SetUint64(mod-1)).Uint64()
	inv := new(big.Int).ModInverse(new(big.Int).SetUint64(n0), new(big.Int).SetUint64(mod))
	if inv == nil {
		return 0
	}
	// n0inv = -n0^-1 mod 2^32
	return uint32(0 - inv.Uint64())
}

func (i *Identity) Certificate() ([]byte, error) {
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: i.Name},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, &i.Private.PublicKey, i.Private)
}

func sanitizeName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "golder"
	}
	return strings.Join(strings.Fields(name), "_")
}
