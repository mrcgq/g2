//internal/protocol/constants.go
package protocol

// 消息类型
const (
	TypeConnect     byte = 0x01
	TypeConnectResp byte = 0x02
	TypeData        byte = 0x03
	TypeDisconnect  byte = 0x04
)

// 网络类型
const (
	NetworkTCP byte = 0x01
	NetworkUDP byte = 0x02
)

// 地址类型
const (
	AddrIPv4   byte = 0x01
	AddrIPv6   byte = 0x02
	AddrDomain byte = 0x03
)

// 状态码
const (
	StatusOK            byte = 0x00
	StatusError         byte = 0x01
	StatusConnectFailed byte = 0x02
)
