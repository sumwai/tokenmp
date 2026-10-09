package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是上游凭据与渠道模型映射的管理动作。

// CredentialInput 是新增上游凭据的输入。
//
// APIKey 是明文，只在写入这一条路径出现；列表走 CredentialView，不回显明文。
type CredentialInput struct {
	MerchantID uint64
	Group      string
	Name       string
	APIKey     string
}

// AddCredential 新增一行上游凭据。
//
// secret 以 {"api_key": "..."} 承载：协议的附加字段后续按需扩展，
// 存储层不解释结构，管理面只写它认识的部分。
func (s *Service) AddCredential(ctx context.Context, in CredentialInput) (uint64, error) {
	if err := requireID("凭据 merchant", in.MerchantID); err != nil {
		return 0, err
	}
	if err := requireString("凭据 group", in.Group); err != nil {
		return 0, err
	}
	if err := requireString("凭据 api-key", in.APIKey); err != nil {
		return 0, err
	}
	//nolint:gosec // G117：这是写入库表的凭据 JSON，字段名由协议约定，不是硬编码凭据。
	secret, err := credential.BuildAPISecret(in.APIKey)
	if err != nil {
		return 0, fmt.Errorf("admin: 编码凭据 secret 失败: %w", err)
	}
	return s.store.InsertCredential(ctx, store.CredentialRow{
		MerchantID: in.MerchantID,
		CredGroup:  in.Group,
		Name:       in.Name,
		Secret:     secret,
	})
}

// ListCredentials 列出全部上游凭据，只给出脱敏前缀。
func (s *Service) ListCredentials(ctx context.Context) ([]CredentialView, error) {
	rows, err := s.store.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]CredentialView, 0, len(rows))
	now := s.now()
	for _, row := range rows {
		kind, expires, expired := credentialState(row.Secret, now)
		views = append(views, CredentialView{
			ID:         row.ID,
			MerchantID: row.MerchantID,
			CredGroup:  row.CredGroup,
			Name:       row.Name,
			Kind:       kind,
			Prefix:     maskSecret(row.Secret),
			Expires:    expires,
			Expired:    expired,
			Enabled:    row.Enabled,
		})
	}
	return views, nil
}

// DisableCredential 停用一行上游凭据。
func (s *Service) DisableCredential(ctx context.Context, id uint64) error {
	if err := requireID("凭据 id", id); err != nil {
		return err
	}
	return s.store.SetCredentialEnabled(ctx, id, false)
}

// EnableCredential 启用一行上游凭据。
//
// 与 DisableCredential 成对：停用是运营动作，误停之后必须能原地恢复，
// 否则只能直接改库或重新写入一份凭据（后者会让凭据 id 与历史流水脱钩）。
func (s *Service) EnableCredential(ctx context.Context, id uint64) error {
	if err := requireID("凭据 id", id); err != nil {
		return err
	}
	return s.store.SetCredentialEnabled(ctx, id, true)
}

// defaultMultiplier 是渠道倍率与账户倍率的默认值，与列的 DEFAULT 1 一致。
const defaultMultiplier = "1"

// ModelMapInput 是写入渠道模型映射的输入。
type ModelMapInput struct {
	ChannelID        uint64
	Model            string
	UpstreamModel    string
	PriceMultiplier  string
	RequestOverrides json.RawMessage
}

// SetModelMap 写入或覆盖一条渠道模型映射。
//
// set 是幂等动作：同一 (channel_id, model) 重复写入只覆盖取值。
// overrides 必须是 JSON 对象，非法形态在进入存储层前就报出。
func (s *Service) SetModelMap(ctx context.Context, in ModelMapInput) (uint64, error) {
	if err := requireID("模型映射 channel", in.ChannelID); err != nil {
		return 0, err
	}
	if err := requireString("模型映射 model", in.Model); err != nil {
		return 0, err
	}
	if err := requireString("模型映射 upstream-model", in.UpstreamModel); err != nil {
		return 0, err
	}
	multiplier := in.PriceMultiplier
	if multiplier == "" {
		multiplier = defaultMultiplier
	}
	if err := requireDecimal("模型映射 multiplier", multiplier); err != nil {
		return 0, err
	}
	overrides := bytes.TrimSpace(in.RequestOverrides)
	if len(overrides) > 0 {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(overrides, &patch); err != nil || patch == nil {
			return 0, invalidf("admin: 模型映射 overrides 必须是 JSON 对象")
		}
	}
	return s.store.UpsertModelMap(ctx, store.ModelMap{
		ChannelID:        in.ChannelID,
		Model:            in.Model,
		UpstreamModel:    in.UpstreamModel,
		PriceMultiplier:  multiplier,
		RequestOverrides: overrides,
	})
}

// ListModelMaps 列出全部渠道模型映射。
func (s *Service) ListModelMaps(ctx context.Context) ([]store.ModelMap, error) {
	return s.store.ListModelMaps(ctx)
}

// DisableModelMap 停用一条渠道模型映射。
func (s *Service) DisableModelMap(ctx context.Context, id uint64) error {
	if err := requireID("模型映射 id", id); err != nil {
		return err
	}
	return s.store.SetModelMapEnabled(ctx, id, false)
}
