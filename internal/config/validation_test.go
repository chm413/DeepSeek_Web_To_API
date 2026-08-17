package config

import (
	"strings"
	"testing"
)

func TestValidateConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "admin",
			cfg:  Config{Admin: AdminConfig{JWTExpireHours: 721}},
			want: "admin.jwt_expire_hours",
		},
		{
			name: "runtime relation",
			cfg: Config{Runtime: RuntimeConfig{
				AccountMaxInflight: 8,
				GlobalMaxInflight:  4,
			}},
			want: "runtime.global_max_inflight must be >= runtime.account_max_inflight",
		},
		{
			name: "responses",
			cfg:  Config{Responses: ResponsesConfig{StoreTTLSeconds: 10}},
			want: "responses.store_ttl_seconds",
		},
		{
			name: "embeddings",
			cfg:  Config{Embeddings: EmbeddingsConfig{Provider: "   "}},
			want: "embeddings.provider",
		},
		{
			name: "auto delete",
			cfg:  Config{AutoDelete: AutoDeleteConfig{Mode: "maybe"}},
			want: "auto_delete.mode",
		},
		{
			name: "current input file",
			cfg:  Config{CurrentInputFile: CurrentInputFileConfig{MinChars: -1}},
			want: "current_input_file.min_chars",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.cfg)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q in error, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateConfigAcceptsLegacyAutoDeleteSessions(t *testing.T) {
	if err := ValidateConfig(Config{AutoDelete: AutoDeleteConfig{Sessions: true}}); err != nil {
		t.Fatalf("expected legacy auto_delete.sessions config to remain valid, got %v", err)
	}
}

func TestValidateAppUpdateRequiresAutoDownloadForAutoApply(t *testing.T) {
	autoApply := true
	if err := ValidateAppUpdateConfig(AppUpdateConfig{AutoApply: &autoApply}); err == nil {
		t.Fatal("expected auto_apply without auto_download to be rejected")
	}
	autoDownload := true
	if err := ValidateAppUpdateConfig(AppUpdateConfig{AutoApply: &autoApply, AutoDownload: &autoDownload}); err != nil {
		t.Fatalf("expected matching automatic update policy to be valid: %v", err)
	}
}
