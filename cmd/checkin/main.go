// checkin.go — LobsterAI 每日签到（全部账号）。
//
// 用法:
//
//	go run ./cmd/checkin        # 或编译后 ./checkin
//
// 需要 LB2A_UPSTREAM_BASE。输出一行一个账号：
//
//	[2026-09-14 09:05:01] [10001] 签到成功 +100 分；剩余 832.35 分
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"lobsterai2api/internal/auth"
	"lobsterai2api/internal/upstream"
)

func main() {
	os.Exit(run())
}

func run() int {
	if os.Getenv("LB2A_UPSTREAM_BASE") == "" {
		fmt.Fprintln(os.Stderr, "checkin: LB2A_UPSTREAM_BASE env not set")
		return 1
	}
	authDir := "./auths"
	if v := os.Getenv("LB2A_AUTH_DIR"); v != "" {
		authDir = v
	}
	auths, err := auth.LoadDir(authDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkin: load auths: %v\n", err)
		return 1
	}
	if len(auths) == 0 {
		fmt.Fprintf(os.Stderr, "没找到账号文件：%s/lobsterai-*.json\n", authDir)
		return 1
	}
	up := upstream.New()
	up.HTTP.Timeout = 30 * time.Second
	if _, err := up.ResolveClientVersion(); err != nil {
		fmt.Printf("解析 clientVersion 失败，本次不签到：%v\n", err)
		return 1
	}
	fails := 0
	now := time.Now().Format("2006-01-02 15:04:05")
	for _, a := range auths {
		line := checkOne(up, a)
		fmt.Printf("[%s] %s\n", now, line)
		if strings.Contains(line, "签到失败") {
			fails++
		}
	}
	if fails > 0 {
		return 1
	}
	return 0
}

func checkOne(up *upstream.Client, a *auth.Auth) string {
	res, err := up.DailyCheckin(a)
	if err != nil {
		return fmt.Sprintf("[%s] 签到失败：%v", a.UID, err)
	}
	line := fmt.Sprintf("[%s] %s", a.UID, res.Message)
	if res.Gained != nil {
		line += fmt.Sprintf(" +%g 分", *res.Gained)
	}
	remain, qerr := up.CreditsRemaining(a)
	if qerr == nil {
		line += fmt.Sprintf("；剩余 %g 分", remain)
	}
	return line
}
