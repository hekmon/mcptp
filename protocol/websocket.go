package protocol

import (
	"time"

	"github.com/coder/websocket"
)

const (
	CompressionMode       = websocket.CompressionContextTakeover
	SubProcessGracePeriod = 10 * time.Second
)
