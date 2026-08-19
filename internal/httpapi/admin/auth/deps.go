package auth

import (
	"sync"

	"DeepSeek_Web_To_API/internal/chathistory"
	adminshared "DeepSeek_Web_To_API/internal/httpapi/admin/shared"
)

type Handler struct {
	Store       adminshared.ConfigStore
	Pool        adminshared.PoolController
	DS          adminshared.DeepSeekCaller
	OpenAI      adminshared.OpenAIChatCaller
	ChatHistory *chathistory.Store

	loginLimiterMu sync.Mutex
	loginLimiter   *loginLimiter
}

var writeJSON = adminshared.WriteJSON
var intFrom = adminshared.IntFrom
