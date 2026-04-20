package protocol

import (
	"time"

	"github.com/coder/websocket"
)

const (
	CompressionMode       = websocket.CompressionContextTakeover
	SubProcessGracePeriod = 5 * time.Second
	BinaryFrameMaxSize    = 32 * 1024 // 32KB
)
