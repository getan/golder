package adb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

var (
	pairingClientName = []byte("adb pair client\x00")
	pairingServerName = []byte("adb pair server\x00")
)

func hkdfSHA256(ikm, salt, info []byte, length int) []byte {
	if len(salt) == 0 {
		salt = make([]byte, 32)
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	prk := mac.Sum(nil)
	mac = hmac.New(sha256.New, prk)
	out := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(out) < length; counter++ {
		mac.Reset()
		mac.Write(previous)
		mac.Write(info)
		mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		out = append(out, previous...)
	}
	return out[:length]
}

type pairingCipher struct {
	aead   cipher.AEAD
	encSeq uint64
	decSeq uint64
}

func newPairingCipher(keyMaterial []byte) (*pairingCipher, error) {
	key := hkdfSHA256(keyMaterial, nil, []byte("adb pairing_auth aes-128-gcm key"), 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &pairingCipher{aead: aead}, nil
}

func (c *pairingCipher) nonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.LittleEndian.PutUint64(nonce[:8], seq)
	return nonce
}

func (c *pairingCipher) Encrypt(plain []byte) []byte {
	sealed := c.aead.Seal(nil, c.nonce(c.encSeq), plain, nil)
	c.encSeq++
	return sealed
}

func (c *pairingCipher) Decrypt(sealed []byte) ([]byte, error) {
	plain, err := c.aead.Open(nil, c.nonce(c.decSeq), sealed, nil)
	if err != nil {
		return nil, err
	}
	c.decSeq++
	return plain, nil
}

type PairingAuth struct {
	spake   *Spake2
	cipher  *pairingCipher
	Message []byte
}

func NewPairingAuth(role SpakeRole, password, seed []byte) (*PairingAuth, error) {
	var myName, theirName []byte
	if role == SpakeAlice {
		myName, theirName = pairingClientName, pairingServerName
	} else {
		myName, theirName = pairingServerName, pairingClientName
	}
	spake := NewSpake2(role, myName, theirName)
	message, err := spake.Generate(password, seed)
	if err != nil {
		return nil, err
	}
	return &PairingAuth{spake: spake, Message: message}, nil
}

func (p *PairingAuth) Init(their []byte) error {
	if p.cipher != nil {
		return fmt.Errorf("pairing cipher already initialized")
	}
	key, err := p.spake.Process(their)
	if err != nil {
		return err
	}
	p.cipher, err = newPairingCipher(key)
	return err
}

func (p *PairingAuth) Encrypt(plain []byte) ([]byte, error) {
	if p.cipher == nil {
		return nil, fmt.Errorf("pairing cipher is not initialized")
	}
	return p.cipher.Encrypt(plain), nil
}

func (p *PairingAuth) Decrypt(sealed []byte) ([]byte, error) {
	if p.cipher == nil {
		return nil, fmt.Errorf("pairing cipher is not initialized")
	}
	return p.cipher.Decrypt(sealed)
}
