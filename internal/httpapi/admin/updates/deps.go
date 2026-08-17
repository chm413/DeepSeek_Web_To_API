package updates

import (
	adminshared "DeepSeek_Web_To_API/internal/httpapi/admin/shared"
	"DeepSeek_Web_To_API/internal/selfupdate"
)

type Handler struct {
	Updater *selfupdate.Manager
}

var writeJSON = adminshared.WriteJSON
