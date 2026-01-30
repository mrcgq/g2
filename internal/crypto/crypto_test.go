package crypto

import (
	"testing"
)

func TestGeneratePSK(t *testing.T) {
	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}
	if len(psk) == 0 {
		t.Fatal("PSK 为空")
	}
	t.Logf("PSK: %s", psk)
}

func TestNewCrypto(t *testing.T) {
	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	c, err := New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	userID := c.GetUserID()
	if userID == [UserIDSize]byte{} {
		t.Fatal("UserID 为空")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	c, err := New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	plaintext := []byte("Hello, Phantom v3!")

	encrypted, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("解密结果不匹配: got %s, want %s", decrypted, plaintext)
	}
}

func TestReplayProtection(t *testing.T) {
	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	c, err := New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	plaintext := []byte("Test replay")
	encrypted, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	// 第一次解密应该成功
	_, err = c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("首次解密失败: %v", err)
	}

	// 重放应该失败
	_, err = c.Decrypt(encrypted)
	if err == nil {
		t.Fatal("重放攻击应该被检测到")
	}
}

func TestInvalidPSK(t *testing.T) {
	psk1, err := GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK1 失败: %v", err)
	}

	psk2, err := GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK2 失败: %v", err)
	}

	c1, err := New(psk1, 30)
	if err != nil {
		t.Fatalf("创建 Crypto1 失败: %v", err)
	}

	c2, err := New(psk2, 30)
	if err != nil {
		t.Fatalf("创建 Crypto2 失败: %v", err)
	}

	plaintext := []byte("Test message")
	encrypted, err := c1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	_, err = c2.Decrypt(encrypted)
	if err == nil {
		t.Fatal("使用错误的 PSK 解密应该失败")
	}
}

func BenchmarkEncrypt(b *testing.B) {
	psk, err := GeneratePSK()
	if err != nil {
		b.Fatalf("生成 PSK 失败: %v", err)
	}

	c, err := New(psk, 30)
	if err != nil {
		b.Fatalf("创建 Crypto 失败: %v", err)
	}

	data := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Encrypt(data)
	}
}

func BenchmarkDecrypt(b *testing.B) {
	psk, err := GeneratePSK()
	if err != nil {
		b.Fatalf("生成 PSK 失败: %v", err)
	}

	c, err := New(psk, 30)
	if err != nil {
		b.Fatalf("创建 Crypto 失败: %v", err)
	}

	data := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 每次生成新的加密数据，因为有重放保护
		encrypted, err := c.Encrypt(data)
		if err != nil {
			b.Fatalf("加密失败: %v", err)
		}
		_, _ = c.Decrypt(encrypted)
	}
}
