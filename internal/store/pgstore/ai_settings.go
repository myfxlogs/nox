package pgstore

// ai_settings.go D-091 返修② R3：AI 配置 DB 化（单行表 id=1）。
// GetAiSettings 读单行；SetAiSettings UPSERT id=1（空 api_key=不改动）。

import (
	"context"
	"time"
)

// AiSettings AI 分析员配置（单行表 id=1）。
type AiSettings struct {
	BaseURL   string
	Model     string
	APIKey    string
	Enabled   bool
	UpdatedAt time.Time
}

// GetAiSettings 读 ai_settings id=1（表初始行 INSERT ON CONFLICT DO NOTHING 保证存在）。
func (s *Store) GetAiSettings(ctx context.Context) (*AiSettings, error) {
	row := s.pool.QueryRow(ctx, `SELECT base_url, model, api_key, enabled, updated_at FROM ai_settings WHERE id = 1`)
	var a AiSettings
	var baseURL, model, apiKey *string
	if err := row.Scan(&baseURL, &model, &apiKey, &a.Enabled, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if baseURL != nil {
		a.BaseURL = *baseURL
	}
	if model != nil {
		a.Model = *model
	}
	if apiKey != nil {
		a.APIKey = *apiKey
	}
	return &a, nil
}

// SetAiSettings UPSERT id=1。apiKey 空串=不改动（保留原值）；非空=覆盖。
func (s *Store) SetAiSettings(ctx context.Context, baseURL, model, apiKey string, enabled bool) error {
	if apiKey == "" {
		_, err := s.pool.Exec(ctx, `
			UPDATE ai_settings SET base_url = $1, model = $2, enabled = $3, updated_at = now() WHERE id = 1`,
			nullIfEmpty(baseURL), nullIfEmpty(model), enabled)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_settings SET base_url = $1, model = $2, api_key = $3, enabled = $4, updated_at = now() WHERE id = 1`,
		nullIfEmpty(baseURL), nullIfEmpty(model), apiKey, enabled)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
