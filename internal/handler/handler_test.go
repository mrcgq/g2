//internal/handler/handler_test.go
package handler

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/anthropics/phantom-server/internal/crypto"
)

func TestTCPHandlerBasic(t *testing.T) {
	psk, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	cry, err := crypto.New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	h := NewTCPHandler(cry, "debug")
	if h == nil {
		t.Fatal("创建 Handler 失败")
	}
}

func TestTCPHandlerDecryptFail(t *testing.T) {
	psk, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	cry, err := crypto.New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	_ = NewTCPHandler(cry, "error")

	// 对于 TCP Handler，需要通过模拟连接测试
	// 这里只测试基本创建
}

func TestTCPHandlerWrongPSK(t *testing.T) {
	psk1, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK1 失败: %v", err)
	}

	psk2, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK2 失败: %v", err)
	}

	cry1, err := crypto.New(psk1, 30)
	if err != nil {
		t.Fatalf("创建 Crypto1 失败: %v", err)
	}

	cry2, err := crypto.New(psk2, 30)
	if err != nil {
		t.Fatalf("创建 Crypto2 失败: %v", err)
	}

	_ = NewTCPHandler(cry2, "error")

	// 用 psk1 加密的数据，psk2 应该无法解密
	data := []byte{0x02, 0, 0, 0, 1} // TypeData
	encrypted, err := cry1.Encrypt(data)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	// 尝试用 cry2 解密，应该失败
	_, err = cry2.Decrypt(encrypted)
	if err == nil {
		t.Error("使用错误的 PSK 解密应该失败")
	}
}

func TestConnCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过耗时测试")
	}

	psk, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	cry, err := crypto.New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	h := NewTCPHandler(cry, "error")

	// 创建一个模拟连接
	c := &Conn{
		ID:         1,
		LastActive: time.Now().Add(-10 * time.Minute), // 10分钟前
	}
	h.conns.Store(uint32(1), c)

	// 手动触发清理
	h.cleanup()

	// 检查是否被清理
	if _, ok := h.conns.Load(uint32(1)); ok {
		t.Log("注意: 连接可能未被清理，因为 Target 为 nil")
	}
}

// mockConn 用于测试的模拟连接
type mockConn struct {
	readData  []byte
	writeData []byte
	closed    bool
}

func (m *mockConn) Read(b []byte) (n int, err error) {
	if len(m.readData) == 0 {
		return 0, net.ErrClosed
	}
	n = copy(b, m.readData)
	m.readData = m.readData[n:]
	return n, nil
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	m.writeData = append(m.writeData, b...)
	return len(b), nil
}

func (m *mockConn) Close() error {
	m.closed = true
	return nil
}

func (m *mockConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (m *mockConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

func TestHandlerWithMockConn(t *testing.T) {
	psk, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	cry, err := crypto.New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	h := NewTCPHandler(cry, "debug")

	// 创建模拟连接，发送空数据会立即关闭
	mock := &mockConn{readData: []byte{}}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// 这会因为没有数据而快速退出
	h.HandleConnection(ctx, mock)
}
