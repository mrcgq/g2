package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	PSKSize       = 32
	UserIDSize    = 4
	TimestampSize = 2
	NonceSize     = chacha20poly1305.NonceSize // 12
	TagSize       = chacha20poly1305.Overhead  // 16
	HeaderSize    = UserIDSize + TimestampSize // 6
)

// Crypto 加密器
type Crypto struct {
	psk        []byte
	userID     [UserIDSize]byte
	timeWindow int

	aeadCache   sync.Map // window -> cipher.AEAD
	replayCache sync.Map // nonce -> time.Time
}

// New 创建加密器
func New(pskBase64 string, timeWindow int) (*Crypto, error) {
	psk, err := base64.StdEncoding.DecodeString(pskBase64)
	if err != nil {
		return nil, fmt.Errorf("PSK 解码失败: %w", err)
	}
	if len(psk) != PSKSize {
		return nil, fmt.Errorf("PSK 长度必须是 %d 字节", PSKSize)
	}

	c := &Crypto{
		psk:        psk,
		timeWindow: timeWindow,
	}

	// 派生 UserID
	reader := hkdf.New(sha256.New, psk, nil, []byte("phantom-userid-v3"))
	io.ReadFull(reader, c.userID[:])

	// 启动清理
	go c.cleanupLoop()

	return c, nil
}

// GetUserID 返回 UserID
func (c *Crypto) GetUserID() [UserIDSize]byte {
	return c.userID
}

// Encrypt 加密数据
func (c *Crypto) Encrypt(plaintext []byte) ([]byte, error) {
	window := c.currentWindow()
	aead := c.getAEAD(window)

	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	timestamp := uint16(time.Now().Unix() & 0xFFFF)

	// 输出: UserID(4) + Timestamp(2) + Nonce(12) + Ciphertext + Tag(16)
	output := make([]byte, HeaderSize+NonceSize+len(plaintext)+TagSize)
	copy(output[:UserIDSize], c.userID[:])
	binary.BigEndian.PutUint16(output[UserIDSize:HeaderSize], timestamp)
	copy(output[HeaderSize:HeaderSize+NonceSize], nonce)

	// AAD = Header
	aead.Seal(output[HeaderSize+NonceSize:HeaderSize+NonceSize], nonce, plaintext, output[:HeaderSize])

	return output, nil
}

// Decrypt 解密数据
func (c *Crypto) Decrypt(data []byte) ([]byte, error) {
	minSize := HeaderSize + NonceSize + TagSize
	if len(data) < minSize {
		return nil, fmt.Errorf("数据太短")
	}

	// 验证 UserID
	var userID [UserIDSize]byte
	copy(userID[:], data[:UserIDSize])
	if userID != c.userID {
		return nil, fmt.Errorf("UserID 不匹配")
	}

	// 验证时间戳
	timestamp := binary.BigEndian.Uint16(data[UserIDSize:HeaderSize])
	if !c.validateTimestamp(timestamp) {
		return nil, fmt.Errorf("时间戳无效")
	}

	// 重放检查
	nonce := data[HeaderSize : HeaderSize+NonceSize]
	nonceKey := string(nonce)
	if _, exists := c.replayCache.LoadOrStore(nonceKey, time.Now()); exists {
		return nil, fmt.Errorf("重放攻击")
	}

	ciphertext := data[HeaderSize+NonceSize:]
	header := data[:HeaderSize]

	// 尝试多个时间窗口
	for _, window := range c.validWindows() {
		aead := c.getAEAD(window)
		if plaintext, err := aead.Open(nil, nonce, ciphertext, header); err == nil {
			return plaintext, nil
		}
	}

	return nil, fmt.Errorf("解密失败")
}

func (c *Crypto) currentWindow() int64 {
	return time.Now().Unix() / int64(c.timeWindow)
}

func (c *Crypto) validWindows() []int64 {
	w := c.currentWindow()
	return []int64{w - 1, w, w + 1}
}

func (c *Crypto) getAEAD(window int64) *chacha20poly1305.XChaCha20Poly1305 {
	if v, ok := c.aeadCache.Load(window); ok {
		return v.(*chacha20poly1305.XChaCha20Poly1305)
	}

	// 派生密钥
	salt := make([]byte, 8)
	binary.BigEndian.PutUint64(salt, uint64(window))
	reader := hkdf.New(sha256.New, c.psk, salt, []byte("phantom-key-v3"))
	key := make([]byte, chacha20poly1305.KeySize)
	io.ReadFull(reader, key)

	aead, _ := chacha20poly1305.New(key)
	c.aeadCache.Store(window, aead)
	return aead
}

func (c *Crypto) validateTimestamp(ts uint16) bool {
	current := uint16(time.Now().Unix() & 0xFFFF)
	diff := int(current) - int(ts)

	// 处理环绕
	if diff < -32768 {
		diff += 65536
	} else if diff > 32768 {
		diff -= 65536
	}
	if diff < 0 {
		diff = -diff
	}

	return diff <= c.timeWindow*2
}

func (c *Crypto) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		cw := c.currentWindow()

		// 清理重放缓存
		c.replayCache.Range(func(key, value interface{}) bool {
			if t := value.(time.Time); now.Sub(t) > 2*time.Minute {
				c.replayCache.Delete(key)
			}
			return true
		})

		// 清理 AEAD 缓存
		c.aeadCache.Range(func(key, value interface{}) bool {
			if cw-key.(int64) > 2 {
				c.aeadCache.Delete(key)
			}
			return true
		})
	}
}

// GeneratePSK 生成新的 PSK
func GeneratePSK() (string, error) {
	psk := make([]byte, PSKSize)
	if _, err := rand.Read(psk); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(psk), nil
}
