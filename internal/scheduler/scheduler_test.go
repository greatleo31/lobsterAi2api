package scheduler

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextFireSameDay(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 9, 20, 10, 30, 0, 0, loc)
	got := nextFire(now, []int{9, 21})
	want := time.Date(2026, 9, 20, 21, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("nextFire = %v, want %v", got, want)
	}
}

func TestNextFireTomorrow(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 9, 20, 22, 1, 0, 0, loc)
	got := nextFire(now, []int{9, 21})
	want := time.Date(2026, 9, 21, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("nextFire = %v, want %v", got, want)
	}
}

func TestMissedSlots(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 9, 20, 11, 14, 0, 0, loc)
	hist := []JobRun{{
		Job:     "checkin",
		At:      time.Date(2026, 9, 20, 11, 14, 5, 0, loc),
		Trigger: "startup",
	}}
	missed := missedSlots(now, []int{9, 21}, "checkin", hist)
	if len(missed) != 1 || missed[0] != "checkin 09:00" {
		t.Fatalf("missed = %v, want [checkin 09:00]", missed)
	}
	hist = append(hist, JobRun{
		Job:     "checkin",
		At:      time.Date(2026, 9, 20, 9, 0, 1, 0, loc),
		Trigger: "schedule",
	})
	if got := missedSlots(now, []int{9, 21}, "checkin", hist); len(got) != 0 {
		t.Fatalf("after schedule run, missed = %v", got)
	}
}

func TestPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "schedule.json")
	s := New(Config{StateFile: fp, CheckinHours: []int{9}, KeepaliveHours: []int{22}})
	s.record(JobRun{
		Job:     "checkin",
		At:      time.Now(),
		Trigger: "schedule",
		OK:      true,
		Accounts: []AccountRun{
			{UID: "10001", OK: true, Message: "今天已签到，跳过"},
		},
	})
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	var pf persistFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		t.Fatal(err)
	}
	if len(pf.History) != 1 || pf.History[0].Job != "checkin" {
		t.Fatalf("file history = %+v", pf.History)
	}
	s2 := New(Config{StateFile: fp})
	st := s2.Status()
	if st.LastCheckin == nil || st.LastCheckin.Accounts[0].UID != "10001" {
		t.Fatalf("reloaded status = %+v", st)
	}
	if !st.CheckinToday {
		t.Fatal("run recorded just now, checkin_today should be true")
	}
	if st.KeepaliveToday {
		t.Fatal("no keepalive recorded, keepalive_today should be false")
	}
	// /status 的 history 必须是精简形态：只有结果摘要，不带逐账号明细
	if len(st.History) != 1 || st.History[0].Summary != "签到成功" || st.History[0].OK != true {
		t.Fatalf("compact history = %+v", st.History)
	}
	rawHist, _ := json.Marshal(st.History)
	if bytes.Contains(rawHist, []byte("accounts")) || bytes.Contains(rawHist, []byte("message")) {
		t.Fatalf("compact history leaks account detail: %s", rawHist)
	}
}

func TestStatusJSONHasSummary(t *testing.T) {
	s := New(Config{CheckinHours: []int{9, 21}, KeepaliveHours: []int{22}})
	st := s.Status()
	if st.Summary == "" {
		t.Fatal("empty summary")
	}
	if st.Running {
		t.Fatal("Run not started, running should be false")
	}
	if st.Missed == nil {
		t.Fatal("missed should be empty slice, not nil")
	}
	raw, _ := json.Marshal(st)
	if len(raw) < 20 {
		t.Fatalf("status json too small: %s", raw)
	}
}
