package browser

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

// 浏览器池。
//
// 起因：以前每个请求都 newBrowser() + defer Close()，冷启一个 Chromium 再销毁，
// 实测这一步约 1.5~2 秒，而一轮「搜 4 个词 + 深读 3 篇」要付 7 次。
//
// 但也不能全进程共用一个浏览器：试过，单请求确实快了（搜索 4.2→2.5 秒），
// 可一旦并发就崩——同一个 Chromium 里的多个 tab 抢主线程、后台 tab 还被节流，
// 3 篇并发深读从 21.6 秒劣化到 35.5 秒，还会把 ScrollIntoView 拖到超时。
//
// 所以是池：N 个独立实例，每个请求独占一个，用完归还。隔离性跟「每请求一个
// 浏览器」一样，但省掉冷启。N 跟客户端脚本的并发闸门对齐（xhs.py 的 MAX_JOBS），
// 这样正常情况下不用排队。
//
// 登录二维码那条路径不走池：它要让浏览器活过整个异步等待（最长 4 分钟），
// 生命周期跟请求对不上，继续用 NewBrowser 各自新建。
//
// 闲置回收：这个服务常驻在一台不休眠的机器上，而 3 个空转的 Chromium 实测
// 占 24 个进程、RSS 合计约 2.2 GB。大部分时间根本没有请求，所以空闲超过
// idleTTL 就把实例放掉，下次用再冷启——代价只有那 1.5~2 秒，摊到一次会话里
// 可以忽略。
const (
	defaultPoolSize = 3
	defaultIdleTTL  = 10 * time.Minute
)

type lease struct {
	b     *headless_browser.Browser
	stamp string    // 建这个实例时 cookies.json 的身份
	used  time.Time // 上次归还的时间，闲置回收用
}

var (
	initOnce sync.Once
	slots    chan *lease
)

func poolSize() int {
	if v, err := strconv.Atoi(os.Getenv("XHS_BROWSER_POOL")); err == nil && v > 0 {
		return v
	}
	return defaultPoolSize
}

func idleTTL() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("XHS_BROWSER_IDLE")); err == nil && d > 0 {
		return d
	}
	return defaultIdleTTL
}

func initPool() {
	n := poolSize()
	slots = make(chan *lease, n)
	for i := 0; i < n; i++ {
		slots <- &lease{} // 空壳，第一次被借走时才真的启动浏览器
	}
	logrus.Infof("浏览器池大小: %d，闲置 %s 后回收", n, idleTTL())
	go reapIdle()
}

// reapIdle 放掉闲置太久的实例。
// 只看当前在 channel 里的——借出去的按定义不在里面，碰不到，也就不用加锁。
func reapIdle() {
	ttl := idleTTL()
	for range time.Tick(time.Minute) {
		for i, n := 0, len(slots); i < n; i++ {
			select {
			case l := <-slots:
				if l.b != nil && time.Since(l.used) > ttl {
					logrus.Infof("浏览器闲置超过 %s，释放", ttl)
					l.reset()
				}
				slots <- l
			default:
			}
		}
	}
}

// cookieStamp 用 mtime+size 而不是内容哈希：cookie 文件每次扫码都整体重写，
// 这两个值足够区分，也不用把文件读进来。文件不存在时返回空串——
// DeleteCookies 之后同样该重建。
func cookieStamp() string {
	fi, err := os.Stat(cookies.GetCookiesFilePath())
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

// NewPage 从池里借一个浏览器并开页面。
// 返回的 release 必须 defer 调用：它关掉页面并把浏览器还回池子。
// 借不到会阻塞——池子满员说明并发已经到顶，排队正是想要的行为。
func NewPage(headless bool, options ...Option) (*rod.Page, func()) {
	initOnce.Do(initPool)

	l := <-slots

	// ensure/开页面都可能 panic（启动失败、浏览器已死）。真 panic 了也必须把
	// 名额还回去，否则池子会被慢慢漏空，最后所有请求卡在 <-slots 上。
	defer func() {
		if r := recover(); r != nil {
			l.reset()
			slots <- l
			panic(r)
		}
	}()

	l.ensure(headless, options...)

	page, err := l.tryPage()
	if err != nil {
		// 多半是这个实例死了（被杀、OOM、崩溃）。重建一次再试；
		// 还不行就让它 panic，跟以前每请求新建时的行为一致。
		logrus.Warnf("池中浏览器开页面失败，重建后重试: %v", err)
		l.reset()
		l.ensure(headless, options...)
		page = l.b.NewPage()
	}

	return page, func() {
		_ = page.Close()
		l.used = time.Now()
		slots <- l
	}
}

func (l *lease) ensure(headless bool, options ...Option) {
	if l.b != nil {
		if l.stamp == cookieStamp() {
			return
		}
		logrus.Info("cookies.json 变了，重建池中浏览器")
		l.reset()
	}
	l.stamp = cookieStamp()
	l.b = NewBrowser(headless, options...)
}

func (l *lease) reset() {
	if l.b == nil {
		return
	}
	// 浏览器已经死掉时 Close 内部的 MustClose 会 panic，这里不关心结果
	func() {
		defer func() { _ = recover() }()
		l.b.Close()
	}()
	l.b = nil
}

func (l *lease) tryPage() (p *rod.Page, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return l.b.NewPage(), nil
}
