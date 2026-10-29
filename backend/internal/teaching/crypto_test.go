package teaching

import (
	"strings"
	"testing"
)

func TestCredentialEncryptionRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	encoded, err := encryptCredential(secret, "internal-teaching-credential")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(encoded, "internal-teaching-credential") {
		t.Fatal("ciphertext leaks plaintext")
	}
	decoded, err := decryptCredential(secret, encoded)
	if err != nil || decoded != "internal-teaching-credential" {
		t.Fatalf("decrypt = %q, %v", decoded, err)
	}
	// A second encryption uses a fresh nonce.
	again, _ := encryptCredential(secret, "internal-teaching-credential")
	if again == encoded {
		t.Fatal("two encryptions reused a nonce")
	}
	other, _ := encryptCredential([]byte("ffffffffffffffffffffffffffffffff"), "x")
	if _, err := decryptCredential(secret, other); err == nil {
		t.Fatal("decrypting with the wrong key must fail")
	}
}

func TestHintModeInstructionNeverPermitsAnswerLeak(t *testing.T) {
	instruction := modeInstruction("hint", true)
	for _, required := range []string{"提示模式", "不透露答案", "不输出参考答案"} {
		if !strings.Contains(instruction, required) {
			t.Fatalf("hint instruction missing %q: %s", required, instruction)
		}
	}
}
