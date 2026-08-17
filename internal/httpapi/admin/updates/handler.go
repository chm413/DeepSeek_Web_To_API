package updates

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/selfupdate"
)

func (h *Handler) getUpdates(w http.ResponseWriter, _ *http.Request) {
	if h == nil || h.Updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "self-update service is unavailable"})
		return
	}
	writeUpdateResponse(w, http.StatusOK, h.Updater.Status(), h.Updater.Settings())
}

func (h *Handler) checkUpdates(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "self-update service is unavailable"})
		return
	}
	if _, err := h.Updater.Check(r.Context()); err != nil {
		writeUpdateError(w, err)
		return
	}
	writeUpdateResponse(w, http.StatusOK, h.Updater.Status(), h.Updater.Settings())
}

func (h *Handler) downloadUpdate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "self-update service is unavailable"})
		return
	}
	tag, err := optionalTag(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.Updater.Download(r.Context(), tag); err != nil {
		writeUpdateError(w, err)
		return
	}
	writeUpdateResponse(w, http.StatusOK, h.Updater.Status(), h.Updater.Settings())
}

func (h *Handler) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "self-update service is unavailable"})
		return
	}
	tag, err := optionalTag(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if tag == "" {
		tag = h.Updater.Status().DownloadedTag
	}
	status, err := h.Updater.Apply(tag)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeUpdateResponse(w, http.StatusAccepted, status, h.Updater.Settings())
	if err := h.Updater.ScheduleRestart(350 * time.Millisecond); err != nil {
		// The candidate remains staged and pending, so an operator can retry
		// after resolving the local runtime issue instead of losing it.
		config.Logger.Warn("[self_update] apply completed but restart was not scheduled", "error", err)
	}
}

func (h *Handler) rollbackUpdate(w http.ResponseWriter, _ *http.Request) {
	if h == nil || h.Updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "self-update service is unavailable"})
		return
	}
	status, err := h.Updater.Rollback()
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeUpdateResponse(w, http.StatusAccepted, status, h.Updater.Settings())
	if err := h.Updater.ScheduleRestart(350 * time.Millisecond); err != nil {
		config.Logger.Warn("[self_update] rollback completed but restart was not scheduled", "error", err)
	}
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "self-update service is unavailable"})
		return
	}
	var raw map[string]any
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := decoder.Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	if nested, ok := raw["settings"].(map[string]any); ok {
		raw = nested
	}
	patch, err := settingsPatch(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	current := h.Updater.Settings()
	willAutoDownload := current.AutoDownload
	if patch.AutoDownload != nil {
		willAutoDownload = *patch.AutoDownload
	}
	willAutoApply := current.AutoApply
	if patch.AutoApply != nil {
		willAutoApply = *patch.AutoApply
	}
	if willAutoApply && !willAutoDownload {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "app_update.auto_apply requires app_update.auto_download"})
		return
	}
	status, err := h.Updater.UpdateSettings(patch)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeUpdateResponse(w, http.StatusOK, status, h.Updater.Settings())
}

func optionalTag(r *http.Request) (string, error) {
	if r == nil || r.Body == nil {
		return "", nil
	}
	var raw struct {
		Tag string `json:"tag"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	if err := decoder.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", errors.New("invalid json")
	}
	return strings.TrimSpace(raw.Tag), nil
}

func settingsPatch(raw map[string]any) (selfupdate.SettingsPatch, error) {
	patch := selfupdate.SettingsPatch{}
	if value, exists := raw["enabled"]; exists {
		parsed, ok := asBool(value)
		if !ok {
			return patch, errors.New("enabled must be a boolean")
		}
		patch.Enabled = &parsed
	}
	if value, exists := raw["auto_download"]; exists {
		parsed, ok := asBool(value)
		if !ok {
			return patch, errors.New("auto_download must be a boolean")
		}
		patch.AutoDownload = &parsed
	}
	if value, exists := raw["auto_apply"]; exists {
		parsed, ok := asBool(value)
		if !ok {
			return patch, errors.New("auto_apply must be a boolean")
		}
		patch.AutoApply = &parsed
	}
	if value, exists := raw["check_interval_minutes"]; exists {
		parsed, ok := asInt(value)
		if !ok {
			return patch, errors.New("check_interval_minutes must be an integer")
		}
		patch.CheckIntervalMinutes = &parsed
	}
	return patch, nil
}

func asBool(value any) (bool, bool) {
	parsed, ok := value.(bool)
	return parsed, ok
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil && parsed == int64(int(parsed))
	default:
		return 0, false
	}
}

func writeUpdateResponse(w http.ResponseWriter, httpStatus int, status selfupdate.Status, settings selfupdate.Settings) {
	latestTag := ""
	latestAsset := ""
	downloadable := false
	if status.Available != nil {
		latestTag = status.Available.Tag
		latestAsset = status.Available.AssetName
		downloadable = status.Available.Downloadable
	}
	checkedAt := int64(0)
	if !status.LastCheckedAt.IsZero() {
		checkedAt = status.LastCheckedAt.Unix()
	}
	writeJSON(w, httpStatus, map[string]any{
		"success": true,
		"settings": map[string]any{
			"enabled":                settings.Enabled,
			"auto_download":          settings.AutoDownload,
			"auto_apply":             settings.AutoApply,
			"check_interval_minutes": settings.CheckIntervalMinutes,
		},
		"status": map[string]any{
			"supported":         status.ContainerManaged && status.CanInstall,
			"container_managed": status.ContainerManaged,
			"current_version":   status.CurrentVersion,
			"current_tag":       status.CurrentTag,
			"latest_tag":        latestTag,
			"latest_asset":      latestAsset,
			"downloadable":      downloadable,
			"update_available":  status.UpdateAvailable,
			"downloaded_tag":    status.DownloadedTag,
			"installed_tag":     status.InstalledTag,
			"previous_tag":      status.PreviousTag,
			"pending_tag":       status.PendingTag,
			"failed_tag":        status.FailedTag,
			"checking":          status.Checking,
			"downloading":       status.Downloading,
			"applying":          status.PendingTag != "",
			"checked_at_unix":   checkedAt,
			"last_error":        status.LastError,
			"repository":        status.Repository,
		},
	})
}

func writeUpdateError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, selfupdate.ErrContainerRequired),
		errors.Is(err, selfupdate.ErrNoUpdateAvailable),
		errors.Is(err, selfupdate.ErrUpdateNotDownloaded),
		errors.Is(err, selfupdate.ErrRestartNotConfigured):
		status = http.StatusConflict
	case errors.Is(err, selfupdate.ErrOperationInProgress):
		status = http.StatusConflict
	case errors.Is(err, selfupdate.ErrInvalidRelease):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{"detail": fmt.Sprint(err)})
}
