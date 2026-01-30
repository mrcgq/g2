package handler

import (
	"net"
	"testing"
	"time"

	"github.com/anthropics/phantom-server/internal/crypto"
	"github.com/anthropics/phantom-server/internal/protocol"
)

func TestHandlerBasic(t *testing.T) {
	psk, _ := crypto.GeneratePSK()
	cry, _ := crypto.New(psk, 30)
	h := New(cry, "debug")

	// 设置模拟发送
	var sent []byte
	h.SetSender(func(data []byte, addr *net.UDPAddr) error {
		sent = data
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

	encrypted, _ := cry.Encrypt(connectData)
	from := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}

	resp := h.HandlePacket(encrypted, from)

	if resp == nil {
		t.Log("收到响应（可能是连接失败的响应）")
	}
}

func TestConnCleanup(t *testing.T) {
	psk, _ := crypto.GeneratePSK()
	cry, _ := crypto.New(psk, 30)
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
		t.Error("过期连接应该被清理")
	}
}
