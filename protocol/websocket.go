package protocol

import (
	"time"

	"github.com/coder/websocket"
)

const (
	DefaultPort           = 8623
	CompressionMode       = websocket.CompressionContextTakeover
	SubProcessGracePeriod = 5 * time.Second
	BinaryFrameMaxSize    = 64 * 1024 // 64KB (matches typical kernel pipe buffer)
)
