// Package scheduler 定时任务：每日签到（09/21点）+ token keepalive（22点）。
// 签到成功后重新查余额，余额 > 0 的冷却账号自动解冻。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lobsterai2api/internal/pool"
	"lobsterai2api/internal/upstream"
)

const historyCap = 20

// Config 调度器依赖。
type Config struct {
	Pool           *pool.Pool
	Upstream       *upstream.Client
	CheckinHours   []int  // 默认 [9, 21]
	KeepaliveHours []int  // 默认 [22]
	StateFile      string // 可选；记录最近几次执行，供 /status 查看
}

// AccountRun 单个账号在一次任务里的结果。
type AccountRun struct {
	UID     string   `json:"uid"`
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Gained  *float64 `json:"gained,omitempty"`
	Remain  *float64 `json:"remain,omitempty"`
}

// JobRun 一次签到或 keepalive 的快照。
type JobRun struct {
	Job      string       `json:"job"` // checkin | keepalive
	At       time.Time    `json:"at"`
	Trigger  string       `json:"trigger"` // startup | schedule | manual
	OK       bool         `json:"ok"`
	Accounts []AccountRun `json:"accounts"`
}

// HistoryEntry /status 历史条目的精简形态：只给结果，不带逐账号明细
//（明细只保留在最近一次的 last_checkin / last_keepalive 和落盘的 schedule.json 里）。
type HistoryEntry struct {
	Job     string    `json:"job"`
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Summary string    `json:"summary"` // 如 "签到成功" / "保活失败"
}

// Status /status 里的 schedule 段。
type Status struct {
	Running        bool           `json:"running"`
	StartedAt      string         `json:"started_at,omitempty"`
	CheckinHours   []int          `json:"checkin_hours"`
	KeepaliveHours []int          `json:"keepalive_hours"`
	NextCheckin    string         `json:"next_checkin,omitempty"`
	NextKeepalive  string         `json:"next_keepalive,omitempty"`
	CheckinToday   bool           `json:"checkin_today"`
	KeepaliveToday bool           `json:"keepalive_today"`
	Missed         []string       `json:"missed"`
	LastCheckin    *JobRun        `json:"last_checkin,omitempty"`
	LastKeepalive  *JobRun        `json:"last_keepalive,omitempty"`
	History        []HistoryEntry `json:"history"`
	Summary        string         `json:"summary"`
}

type persistFile struct {
	History []JobRun `json:"history"`
}

// Scheduler 调度器。
type Scheduler struct {
	cfg       Config
	mu        sync.Mutex
	running   bool
	startedAt time.Time
	history   []JobRun
}

// New 构建。
func New(cfg Config) *Scheduler {
	if len(cfg.CheckinHours) == 0 {
		cfg.CheckinHours = []int{9, 21}
	}
	if len(cfg.KeepaliveHours) == 0 {
		cfg.KeepaliveHours = []int{22}
	}
	s := &Scheduler{cfg: cfg}
	s.load()
	return s
}

// nextFire 返回 now 之后最近的一个整点触发时间；hours 为本地小时（0-23）。
func nextFire(now time.Time, hours []int) time.Time {
	var earliest time.Time
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func sameLocalDay(a, b time.Time) bool {
	ay, am, ad := a.In(b.Location()).Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func lastOf(history []JobRun, job string) *JobRun {
	for i := range history {
		if history[i].Job == job {
			cp := history[i]
			return &cp
		}
	}
	return nil
}

func ranToday(history []JobRun, job string, now time.Time) bool {
	for _, r := range history {
		if r.Job == job && sameLocalDay(r.At, now) {
			return true
		}
	}
	return false
}

// missedSlots 今天已经过点、但 history 里没有对应整点（trigger=schedule）的任务。
func missedSlots(now time.Time, hours []int, job string, history []JobRun) []string {
	var out []string
	for _, h := range hours {
		slot := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !now.After(slot) {
			continue
		}
		found := false
		for _, r := range history {
			if r.Job != job || r.Trigger != "schedule" {
				continue
			}
			if sameLocalDay(r.At, now) && r.At.Hour() == h {
				found = true
				break
			}
		}
		if !found {
			out = append(out, fmt.Sprintf("%s %02d:00", job, h))
		}
	}
	return out
}

func summarize(st Status, now time.Time) string {
	var parts []string
	if st.Running {
		parts = append(parts, "调度循环运行中")
	} else {
		parts = append(parts, "调度循环未启动（进程没在跑或刚起来）")
	}
	if st.CheckinToday {
		parts = append(parts, "今日已签到")
	} else if now.Hour() >= 9 {
		parts = append(parts, "今日尚未签到")
	} else {
		parts = append(parts, "今日签到未到点")
	}
	if st.KeepaliveToday {
		parts = append(parts, "今日已保活")
	} else if now.Hour() >= 22 {
		parts = append(parts, "今日尚未保活")
	}
	if st.LastCheckin != nil {
		tag := "失败"
		if st.LastCheckin.OK {
			tag = "成功"
		}
		parts = append(parts, fmt.Sprintf("上次签到 %s %s", st.LastCheckin.At.Format("01-02 15:04"), tag))
	} else {
		parts = append(parts, "没有签到记录")
	}
	if st.NextCheckin != "" {
		parts = append(parts, "下次签到 "+st.NextCheckin)
	}
	if len(st.Missed) > 0 {
		parts = append(parts, "错过 "+fmt.Sprint(st.Missed))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "；"
		}
		out += p
	}
	return out
}

// compactRun 把一次执行的完整记录压成 /status 历史条目。
func compactRun(r JobRun) HistoryEntry {
	label := "保活"
	if r.Job == "checkin" {
		label = "签到"
	}
	tag := "失败"
	if r.OK {
		tag = "成功"
	}
	return HistoryEntry{Job: r.Job, At: r.At, OK: r.OK, Summary: label + tag}
}

// Status 供 /status 读取；不阻塞任务执行。
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	hist := append([]JobRun(nil), s.history...)
	st := Status{
		Running:        s.running,
		StartedAt:      rfc3339(s.startedAt),
		CheckinHours:   append([]int(nil), s.cfg.CheckinHours...),
		KeepaliveHours: append([]int(nil), s.cfg.KeepaliveHours...),
		NextCheckin:    rfc3339(nextFire(now, s.cfg.CheckinHours)),
		NextKeepalive:  rfc3339(nextFire(now, s.cfg.KeepaliveHours)),
		CheckinToday:   ranToday(hist, "checkin", now),
		KeepaliveToday: ranToday(hist, "keepalive", now),
		LastCheckin:    lastOf(hist, "checkin"),
		LastKeepalive:  lastOf(hist, "keepalive"),
	}
	for _, r := range hist {
		st.History = append(st.History, compactRun(r))
	}
	st.Missed = append(st.Missed, missedSlots(now, s.cfg.CheckinHours, "checkin", hist)...)
	st.Missed = append(st.Missed, missedSlots(now, s.cfg.KeepaliveHours, "keepalive", hist)...)
	if st.Missed == nil {
		st.Missed = []string{}
	}
	if st.History == nil {
		st.History = []HistoryEntry{}
	}
	st.Summary = summarize(st, now)
	return st
}

func (s *Scheduler) record(run JobRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append([]JobRun{run}, s.history...)
	if len(s.history) > historyCap {
		s.history = s.history[:historyCap]
	}
	s.saveLocked()
}

func (s *Scheduler) load() {
	if s.cfg.StateFile == "" {
		return
	}
	raw, err := os.ReadFile(s.cfg.StateFile)
	if err != nil {
		return
	}
	var pf persistFile
	if json.Unmarshal(raw, &pf) != nil {
		return
	}
	if len(pf.History) > historyCap {
		pf.History = pf.History[:historyCap]
	}
	s.history = pf.History
}

func (s *Scheduler) saveLocked() {
	if s.cfg.StateFile == "" {
		return
	}
	raw, err := json.MarshalIndent(persistFile{History: s.history}, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.cfg.StateFile); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := s.cfg.StateFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.cfg.StateFile)
}

// Run 主循环，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	s.running = true
	s.startedAt = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	all := append(append([]int{}, s.cfg.CheckinHours...), s.cfg.KeepaliveHours...)
	for {
		next := nextFire(time.Now(), all)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			h := time.Now().Hour()
			if contains(s.cfg.CheckinHours, h) {
				s.RunCheckin("schedule")
			}
			if contains(s.cfg.KeepaliveHours, h) {
				s.RunKeepalive("schedule")
			}
		}
	}
}

func contains(hours []int, h int) bool {
	for _, v := range hours {
		if v == h {
			return true
		}
	}
	return false
}

func formatCheckin(uid string, res upstream.CheckinResult, remain *float64, err error) string {
	if err != nil {
		return fmt.Sprintf("[%s] 签到失败：%v", uid, err)
	}
	line := fmt.Sprintf("[%s] %s", uid, res.Message)
	if res.Gained != nil {
		line += fmt.Sprintf(" +%g 分", *res.Gained)
	}
	if remain != nil {
		line += fmt.Sprintf("；剩余 %g 分", *remain)
	}
	return line
}

func checkinMessage(res upstream.CheckinResult, remain *float64, err error) string {
	if err != nil {
		return "签到失败：" + err.Error()
	}
	line := res.Message
	if res.Gained != nil {
		line += fmt.Sprintf(" +%g 分", *res.Gained)
	}
	if remain != nil {
		line += fmt.Sprintf("；剩余 %g 分", *remain)
	}
	return line
}

// RunCheckinNow 立即签到（兼容旧调用，记为 manual）。
func (s *Scheduler) RunCheckinNow() {
	s.RunCheckin("manual")
}

// RunKeepaliveNow 立即保活（兼容旧调用，记为 manual）。
func (s *Scheduler) RunKeepaliveNow() {
	s.RunKeepalive("manual")
}

// RunCheckin 立即对所有账号执行签到 + 余额刷新 + 解冻。
// trigger: startup / schedule / manual。冷却中的账号也参与；禁用的跳过。
func (s *Scheduler) RunCheckin(trigger string) {
	if trigger == "" {
		trigger = "manual"
	}
	run := JobRun{Job: "checkin", At: time.Now(), Trigger: trigger, OK: true}
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.AccessToken == "" {
			run.OK = false
			run.Accounts = append(run.Accounts, AccountRun{
				UID: st.UID, OK: false, Message: "无 access token",
			})
			continue
		}
		res, err := s.cfg.Upstream.DailyCheckin(a)
		var remainPtr *float64
		remain, qerr := s.cfg.Upstream.CreditsRemaining(a)
		if qerr != nil {
			log.Printf("quota %s: %v", st.UID, qerr)
		} else {
			remainPtr = &remain
			s.cfg.Pool.ReenableIfCredits(st.UID, int64(remain))
		}
		ar := AccountRun{
			UID:     st.UID,
			OK:      err == nil,
			Message: checkinMessage(res, remainPtr, err),
			Gained:  res.Gained,
			Remain:  remainPtr,
		}
		if err != nil {
			run.OK = false
		}
		run.Accounts = append(run.Accounts, ar)
		log.Print(formatCheckin(st.UID, res, remainPtr, err))
	}
	s.record(run)
}

// RunKeepalive 立即对所有账号刷新 token；session 死亡的自动禁用。
func (s *Scheduler) RunKeepalive(trigger string) {
	if trigger == "" {
		trigger = "manual"
	}
	run := JobRun{Job: "keepalive", At: time.Now(), Trigger: trigger, OK: true}
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshToken == "" {
			run.OK = false
			run.Accounts = append(run.Accounts, AccountRun{
				UID: st.UID, OK: false, Message: "无 refresh token",
			})
			continue
		}
		if err := s.cfg.Upstream.RefreshToken(a); err != nil {
			log.Printf("keepalive %s: %v", st.UID, err)
			run.OK = false
			run.Accounts = append(run.Accounts, AccountRun{
				UID: st.UID, OK: false, Message: err.Error(),
			})
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
				s.cfg.Pool.Disable(st.UID, "refresh session dead")
			}
			continue
		}
		if err := a.SaveAtomic(); err != nil {
			log.Printf("keepalive %s save: %v", st.UID, err)
			run.OK = false
			run.Accounts = append(run.Accounts, AccountRun{
				UID: st.UID, OK: false, Message: "token 已刷新但落盘失败: " + err.Error(),
			})
			continue
		}
		run.Accounts = append(run.Accounts, AccountRun{
			UID: st.UID, OK: true, Message: "token 已刷新",
		})
	}
	s.record(run)
}
