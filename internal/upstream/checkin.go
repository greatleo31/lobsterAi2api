package upstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"lobsterai2api/internal/auth"
)

// 官方桌面端版本通道（与 chat 上游不是同一台主机）。
const defaultVersionAPI = "https://api-overmind.youdao.com/openapi/get/luna/hardware/lobsterai/prod/update"

var (
	versionMu  sync.Mutex
	versionVal string
	versionAt  time.Time
)

var versionRe = regexp.MustCompile(`^\d+(?:\.\d+)*(?:-[0-9A-Za-z.-]+)?$`)

const versionCacheTTL = time.Hour

// CheckinResult 一次签到的业务结果（已签到 / 无活动不算错误）。
type CheckinResult struct {
	Message string   // 签到成功 / 今天已签到，跳过 / 无可用活动…
	Gained  *float64 // 本次发放积分；跳过或响应未带数字时为 nil
}

func versionAPIURL() string {
	if v := os.Getenv("LB2A_UPDATE_API"); v != "" {
		return v
	}
	return defaultVersionAPI
}

func resetVersionCache() {
	versionMu.Lock()
	versionVal = ""
	versionAt = time.Time{}
	versionMu.Unlock()
}

// ResolveClientVersion 从官方更新接口取桌面端版本，缓存 1h。
// 签到 slot 接口要求 User-Agent / clientVersion 与正式客户端一致。
func (c *Client) ResolveClientVersion() (string, error) {
	versionMu.Lock()
	if versionVal != "" && time.Since(versionAt) < versionCacheTTL {
		v := versionVal
		versionMu.Unlock()
		return v, nil
	}
	versionMu.Unlock()

	req, err := http.NewRequest(http.MethodGet, versionAPIURL(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("%T: %v", err, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(raw), 120))
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Value struct {
				Version string `json:"version"`
			} `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	v := strings.TrimSpace(env.Data.Value.Version)
	if !versionRe.MatchString(v) {
		return "", fmt.Errorf("官方更新接口返回的版本格式异常：%q", v)
	}
	versionMu.Lock()
	versionVal = v
	versionAt = time.Now()
	versionMu.Unlock()
	return v, nil
}

func (c *Client) checkinJSON(method, path, token, ver string, body any) (json.RawMessage, error) {
	base := ServerBase()
	if base == "" {
		return nil, fmt.Errorf("LB2A_UPSTREAM_BASE not set")
	}
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequest(method, base+path, rdr)
	} else {
		req, err = http.NewRequest(method, base+path, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LobsterAI/"+ver)
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, fmt.Errorf("data 为空（accessToken 可能已失效）")
	}
	return data, nil
}

func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func gainedFrom(result map[string]any) *float64 {
	if result == nil {
		return nil
	}
	for _, k := range []string{"creditsGranted", "rewardCredits", "credits"} {
		switch v := result[k].(type) {
		case float64:
			x := v
			return &x
		case json.Number:
			f, err := v.Float64()
			if err == nil {
				return &f
			}
		}
	}
	return nil
}

func containsAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// DailyCheckin 走桌面端活动槽：slot → context → actions/check_in。
// 今天已签到或槽位不可用返回 Message、err=nil。
func (c *Client) DailyCheckin(a *auth.Auth) (CheckinResult, error) {
	ver, err := c.ResolveClientVersion()
	if err != nil {
		return CheckinResult{}, fmt.Errorf("解析 clientVersion 失败：%w", err)
	}
	q := url.Values{
		"placement":           {"desktop_sidebar"},
		"clientVersion":       {ver},
		"containerApiVersion": {"2"},
		"platform":            {"win32"},
	}
	slotRaw, err := c.checkinJSON(http.MethodGet, "/api/client-activities/slot?"+q.Encode(), a.AccessToken, ver, nil)
	if err != nil {
		return CheckinResult{}, err
	}
	var slot struct {
		SlotState string `json:"slotState"`
		Activity  *struct {
			ActivityCode   string `json:"activityCode"`
			ConfigRevision any    `json:"configRevision"`
		} `json:"activity"`
	}
	if err := json.Unmarshal(slotRaw, &slot); err != nil {
		return CheckinResult{}, fmt.Errorf("slot parse: %w", err)
	}
	if slot.SlotState != "available" || slot.Activity == nil || slot.Activity.ActivityCode == "" {
		return CheckinResult{Message: fmt.Sprintf("无可用活动（slotState=%q）", slot.SlotState)}, nil
	}
	code := slot.Activity.ActivityCode
	rev := slot.Activity.ConfigRevision
	ctxPath := "/api/client-activities/" + url.PathEscape(code) + "/context?configRevision=" + url.QueryEscape(fmt.Sprint(rev))
	ctxRaw, err := c.checkinJSON(http.MethodGet, ctxPath, a.AccessToken, ver, nil)
	if err != nil {
		return CheckinResult{}, err
	}
	var ctx struct {
		State *struct {
			ClaimedToday bool `json:"claimedToday"`
		} `json:"state"`
		Actions []string `json:"actions"`
	}
	if err := json.Unmarshal(ctxRaw, &ctx); err != nil {
		return CheckinResult{}, fmt.Errorf("context parse: %w", err)
	}
	claimed := ctx.State != nil && ctx.State.ClaimedToday
	if claimed || !containsAction(ctx.Actions, "check_in") {
		return CheckinResult{Message: "今天已签到，跳过"}, nil
	}
	body := map[string]any{
		"configRevision": rev,
		"idempotencyKey": newIdempotencyKey(),
		"payload":        map[string]any{},
	}
	resRaw, err := c.checkinJSON(http.MethodPost, "/api/client-activities/"+url.PathEscape(code)+"/actions/check_in", a.AccessToken, ver, body)
	if err != nil {
		return CheckinResult{}, err
	}
	var res struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(resRaw, &res); err != nil {
		return CheckinResult{}, fmt.Errorf("check_in parse: %w", err)
	}
	return CheckinResult{Message: "签到成功", Gained: gainedFrom(res.Result)}, nil
}
