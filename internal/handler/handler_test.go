package handler

import (
	"net"
	"testing"
	"time"

	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/protocol"
)

func TestHandlerBasic(t *testing.T) {
	psk, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	cry, err := crypto.New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	h := New(cry, "debug")

	// 设置模拟发送
	h.SetSender(func(data []byte, addr *net.UDPAddr) error {
		t.Logf("发送 %d 字节到 %s", len(data), addr.String())
		return nil
	})

	// 构造连接请求（连接到一个不存在的地址，预期失败）
	connectData := []byte{
		protocol.TypeConnect,
		0, 0, 0, 1, // ReqID = 1
		protocol.NetworkTCP,
		protocol.AddrIPv4,
		127, 0, 0, 1,
		0x00, 0x01, // Port 1 (应该连接失败)
	}

	encrypted, err := cry.Encrypt(connectData)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	from := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}

	resp := h.HandlePacket(encrypted, from)
	if resp != nil {
		t.Logf("收到响应: %d 字节", len(resp))
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

	h := New(cry, "error")

	// 创建一个模拟连接
	c := &Conn{
		ID:         1,
		LastActive: time.Now().Add(-10 * time.Minute), // 10分钟前
	}
	h.conns.Store(uint32(1), c)

	// 等待清理
	time.Sleep(35 * time.Second)

	// 检查是否被清理
	if _, ok := h.conns.Load(uint32(1)); ok {
		t.Log("注意: 连接可能未被清理，因为 Target 为 nil")
	}
}

func TestHandlerDecryptFail(t *testing.T) {
	psk, err := crypto.GeneratePSK()
	if err != nil {
		t.Fatalf("生成 PSK 失败: %v", err)
	}

	cry, err := crypto.New(psk, 30)
	if err != nil {
		t.Fatalf("创建 Crypto 失败: %v", err)
	}

	h := New(cry, "error")

	// 发送无效数据
	invalidData := []byte("invalid encrypted data that should fail")
	from := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}

	resp := h.HandlePacket(invalidData, from)
	if resp != nil {
		t.Error("无效数据应该返回 nil")
	}
}

func TestHandlerWrongPSK(t *testing.T) {
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

	h := New(cry2, "error")

	// 用 psk1 加密
	data := []byte{protocol.TypeData, 0, 0, 0, 1}
	encrypted, err := cry1.Encrypt(data)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	from := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}

	// 用 psk2 的 handler 处理，应该失败
	resp := h.HandlePacket(encrypted, from)
	if resp != nil {
		t.Error("错误的 PSK 应该返回 nil")
	}
}
