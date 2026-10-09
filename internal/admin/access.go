package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// credentialRow 校验凭据字段并折成存储行；新增与修改共用同一套口径。
//
// api-key 为空表示不覆盖 secret（修改路径），新增路径由 AddCredential 先要求非空；
// 归属商家先确认存在：引用不存在会留下归属不明的行。
func (s *Service) credentialRow(ctx context.Context, in CredentialInput) (store.CredentialRow, error) {
	if err := requireID("凭据 merchant", in.MerchantID); err != nil {
		return store.CredentialRow{}, err
	}
	if _, err := s.store.MerchantByID(ctx, in.MerchantID); err != nil {
		return store.CredentialRow{}, requireRow(err, invalidf("admin: 商家 %d 不存在", in.MerchantID))
	}
	if err := requireString("凭据 group", in.Group); err != nil {
		return store.CredentialRow{}, err
	}
	row := store.CredentialRow{
		MerchantID: in.MerchantID,
		CredGroup:  in.Group,
		Name:       in.Name,
	}
	if strings.TrimSpace(in.APIKey) == "" {
		return row, nil
	}
	//nolint:gosec // G117：这是写入库表的凭据 JSON，字段名由协议约定，不是硬编码凭据。
	secret, err := credential.BuildAPISecret(in.APIKey)
	if err != nil {
		return store.CredentialRow{}, fmt.Errorf("admin: 编码凭据 secret 失败: %w", err)
	}
	row.Secret = secret
	return row, nil
}

// AddCredential 新增一行上游凭据。
//
// secret 以 {"api_key": "..."} 承载：协议的附加字段后续按需扩展，
// 存储层不解释结构，管理面只写它认识的部分。
func (s *Service) AddCredential(ctx context.Context, in CredentialInput) (uint64, error) {
	if err := requireString("凭据 api-key", in.APIKey); err != nil {
		return 0, err
	}
	row, err := s.credentialRow(ctx, in)
	if err != nil {
		return 0, err
	}
	return s.store.InsertCredential(ctx, row)
}

// UpdateCredential 改一行凭据的归属、分组与名称；api-key 非空时同时轮换 secret。
//
// 轮换写在原行上而不是新增一行：凭据 id 因此不变，历史流水与凭据的对应关系不断。
func (s *Service) UpdateCredential(ctx context.Context, id uint64, in CredentialInput) error {
	if err := requireID("凭据 id", id); err != nil {
		return err
	}
	if _, err := s.store.CredentialByID(ctx, id); err != nil {
		return requireRow(err, notFoundf("admin: 凭据 %d 不存在", id))
	}
	row, err := s.credentialRow(ctx, in)
	if err != nil {
		return err
	}
	row.ID = id
	return s.store.UpdateCredential(ctx, row)
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
	return s.setCredentialEnabled(ctx, id, false)
}

// EnableCredential 启用一行上游凭据。
//
// 与 DisableCredential 成对：停用是运营动作，误停之后必须能原地恢复，
// 否则只能直接改库或重新写入一份凭据（后者会让凭据 id 与历史流水脱钩）。
func (s *Service) EnableCredential(ctx context.Context, id uint64) error {
	return s.setCredentialEnabled(ctx, id, true)
}

// setCredentialEnabled 是启用 / 停用的公共校验与调用。
func (s *Service) setCredentialEnabled(ctx context.Context, id uint64, enabled bool) error {
	if err := requireID("凭据 id", id); err != nil {
		return err
	}
	if _, err := s.store.CredentialByID(ctx, id); err != nil {
		return requireRow(err, notFoundf("admin: 凭据 %d 不存在", id))
	}
	return s.store.SetCredentialEnabled(ctx, id, enabled)
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

// modelMapRow 校验模型映射字段并折成存储行；写入与修改共用同一套口径。
//
// set 是幂等动作：同一 (channel_id, model) 重复写入只覆盖取值。
// overrides 必须是 JSON 对象，非法形态在进入存储层前就报出。
func (s *Service) modelMapRow(ctx context.Context, in ModelMapInput) (store.ModelMap, error) {
	if err := requireID("模型映射 channel", in.ChannelID); err != nil {
		return store.ModelMap{}, err
	}
	if _, err := s.store.ChannelByID(ctx, in.ChannelID); err != nil {
		return store.ModelMap{}, requireRow(err, invalidf("admin: 渠道 %d 不存在", in.ChannelID))
	}
	if err := requireString("模型映射 model", in.Model); err != nil {
		return store.ModelMap{}, err
	}
	if err := requireString("模型映射 upstream-model", in.UpstreamModel); err != nil {
		return store.ModelMap{}, err
	}
	multiplier := in.PriceMultiplier
	if multiplier == "" {
		multiplier = defaultMultiplier
	}
	if err := requireDecimal("模型映射 multiplier", multiplier); err != nil {
		return store.ModelMap{}, err
	}
	overrides := bytes.TrimSpace(in.RequestOverrides)
	if len(overrides) > 0 {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(overrides, &patch); err != nil || patch == nil {
			return store.ModelMap{}, invalidf("admin: 模型映射 overrides 必须是 JSON 对象")
		}
	}
	return store.ModelMap{
		ChannelID:        in.ChannelID,
		Model:            in.Model,
		UpstreamModel:    in.UpstreamModel,
		PriceMultiplier:  multiplier,
		RequestOverrides: overrides,
	}, nil
}

// SetModelMap 写入或覆盖一条渠道模型映射。
func (s *Service) SetModelMap(ctx context.Context, in ModelMapInput) (uint64, error) {
	row, err := s.modelMapRow(ctx, in)
	if err != nil {
		return 0, err
	}
	return s.store.UpsertModelMap(ctx, row)
}

// UpdateModelMap 改一条模型映射。
//
// 与 SetModelMap 的分工：那个按 (channel_id, model) 这个自然键置位，本方法按主键改行，
// 因此能改模型名本身与归属渠道（按自然键改这两项只会新增一行）。
// 启用位不在本动作里，理由同渠道：启停是独立动作。
func (s *Service) UpdateModelMap(ctx context.Context, id uint64, in ModelMapInput) error {
	if err := requireID("模型映射 id", id); err != nil {
		return err
	}
	if _, err := s.store.ModelMapByID(ctx, id); err != nil {
		return requireRow(err, notFoundf("admin: 模型映射 %d 不存在", id))
	}
	row, err := s.modelMapRow(ctx, in)
	if err != nil {
		return err
	}
	row.ID = id
	return s.store.UpdateModelMap(ctx, row)
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
	if _, err := s.store.ModelMapByID(ctx, id); err != nil {
		return requireRow(err, notFoundf("admin: 模型映射 %d 不存在", id))
	}
	return s.store.SetModelMapEnabled(ctx, id, false)
}
