package libtiktok

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

func TestDecryptTikTokPrivateImageBlob(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	nonce := []byte("123456789012")
	plaintext := []byte{0xff, 0xd8, 0xff, 0xe0, 'J', 'F', 'I', 'F'}

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}

	blob := append(append([]byte{}, nonce...), aead.Seal(nil, nonce, plaintext, nil)...)
	got, err := decryptTikTokPrivateImageBlob(hex.EncodeToString(key), blob)
	if err != nil {
		t.Fatalf("decryptTikTokPrivateImageBlob returned error: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("plaintext = %x, want %x", got, plaintext)
	}
}

func TestDecryptTikTokPrivateImageBlobRejectsTamperedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	nonce := []byte("123456789012")
	plaintext := []byte("private image payload")

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}

	blob := append(append([]byte{}, nonce...), aead.Seal(nil, nonce, plaintext, nil)...)
	blob[len(blob)-1] ^= 0x01

	if _, err := decryptTikTokPrivateImageBlob(hex.EncodeToString(key), blob); err == nil {
		t.Fatal("expected authentication failure, got nil error")
	}
}
