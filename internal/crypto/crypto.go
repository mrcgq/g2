// internal/crypto/crypto.go
package crypto

import (
	"crypto/cipher"
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

	aeadCache sync.Map // window -> cipher.AEAD

	// 改进：分离接收和发送的 Nonce 缓存
	recvNonceCache sync.Map // 接收到的 nonce -> time.Time
	sendNonceCache sync.Map // 发送过的 nonce -> time.Time

	mu sync.RWMutex
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
	if _, err := io.ReadFull(reader, c.userID[:]); err != nil {
		return nil, fmt.Errorf("派生 UserID 失败: %w", err)
	}

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
	aead, err := c.getAEAD(window)
	if err != nil {
		return nil, err
	}

	// 生成唯一 Nonce
	nonce := make([]byte, NonceSize)
	for attempts := 0; attempts < 10; attempts++ {
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		
		// 确保这个 nonce 没有被发送过
		nonceKey := string(nonce)
		if _, exists := c.sendNonceCache.LoadOrStore(nonceKey, time.Now()); !exists {
			break // 找到了唯一的 nonce
		}
		if attempts == 9 {
			return nil, fmt.Errorf("无法生成唯一 Nonce")
		}
	}

	timestamp := uint16(time.Now().Unix() & 0xFFFF)

	// 输出: UserID(4) + Timestamp(2) + Nonce(12) + Ciphertext + Tag(16)
	output := make([]byte, HeaderSize+NonceSize+len(plaintext)+TagSize)
	copy(output[:UserIDSize], c.userID[:])
	binary.BigEndian.PutUint16(output[UserIDSize:HeaderSize], timestamp)
	copy(output[HeaderSize:HeaderSize+NonceSize], nonce)

	// AAD = Header, Seal 会追加密文到 dst
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

	nonce := data[HeaderSize : HeaderSize+NonceSize]
	nonceKey := string(nonce)

	// 重放检查：只检查接收缓存
	if _, exists := c.recvNonceCache.Load(nonceKey); exists {
		return nil, fmt.Errorf("重放攻击")
	}

	ciphertext := data[HeaderSize+NonceSize:]
	header := data[:HeaderSize]

	// 尝试多个时间窗口
	for _, window := range c.validWindows() {
		aead, err := c.getAEAD(window)
		if err != nil {
			continue
		}
		if plaintext, err := aead.Open(nil, nonce, ciphertext, header); err == nil {
			// 解密成功后才记录 nonce
			c.recvNonceCache.Store(nonceKey, time.Now())
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

func (c *Crypto) getAEAD(window int64) (cipher.AEAD, error) {
	if v, ok := c.aeadCache.Load(window); ok {
		return v.(cipher.AEAD), nil
	}

	// 派生密钥
	salt := make([]byte, 8)
	binary.BigEndian.PutUint64(salt, uint64(window))
	reader := hkdf.New(sha256.New, c.psk, salt, []byte("phantom-key-v3"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("派生密钥失败: %w", err)
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("创建 AEAD 失败: %w", err)
	}
	c.aeadCache.Store(window, aead)
	return aead, nil
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
		expireTime := 2 * time.Minute

		// 清理接收 nonce 缓存
		c.recvNonceCache.Range(func(key, value interface{}) bool {
			if t, ok := value.(time.Time); ok && now.Sub(t) > expireTime {
				c.recvNonceCache.Delete(key)
			}
			return true
		})

		// 清理发送 nonce 缓存
		c.sendNonceCache.Range(func(key, value interface{}) bool {
			if t, ok := value.(time.Time); ok && now.Sub(t) > expireTime {
				c.sendNonceCache.Delete(key)
			}
			return true
		})

		// 清理 AEAD 缓存
		c.aeadCache.Range(func(key, value interface{}) bool {
			if w, ok := key.(int64); ok && cw-w > 2 {
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
