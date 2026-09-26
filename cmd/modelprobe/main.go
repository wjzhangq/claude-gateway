// cmd/modelprobe — Model intelligence verification tool.
//
// Usage:
//
//	modelprobe -backends lianxiang-01,dasheng-lianxiang-sc \
//	           -models claude-sonnet-5,claude-opus-5 \
//	           -config config/config.yaml
//
// Sends the 30 Go bug-finding questions from modelprobe-question-bank.md
// directly to each specified backend (bypassing the gateway load balancer)
// and to AWS Bedrock as a control channel. Scores responses per the bank's
// rubric (§ 3.1–3.7) and prints a structured comparison table.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/awsproxy"
)

// ──────────────────────────────────────────────────────────────────
// Question bank (embedded, from modelprobe-question-bank.md appendix)
// ──────────────────────────────────────────────────────────────────

type QuestionMeta struct {
	ID            string
	Title         string
	Difficulty    string // "中" | "难" | "很难"
	Weight        int    // 1, 2, 3
	Clean         bool   // clean questions: expected bug=false
	HitLines      []int  // correct line numbers for bug=true answers
	DistractLines []int  // wrong lines that are specifically penalised
}

// questionBank holds metadata for all 30 questions (machine-readable answers from § appendix).
var questionBank = []QuestionMeta{
	{ID: "q01", Title: "批量下载器", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{51, 52, 53, 54}, DistractLines: []int{55, 57}},
	{ID: "q02", Title: "并发 worker 池", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{55, 56, 57, 58, 59}, DistractLines: []int{41, 62}},
	{ID: "q03", Title: "配置懒加载", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{47, 49, 50, 51}, DistractLines: []int{48}},
	{ID: "q04", Title: "TTL 缓存", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{42, 43, 44, 45, 46, 50, 51, 52, 53, 54}, DistractLines: []int{66}},
	{ID: "q05", Title: "带超时的轮询", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{37, 38, 39, 40}, DistractLines: []int{36}},
	{ID: "q06", Title: "搜索请求扇出", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{35, 37, 38, 39, 40, 41, 42, 43}, DistractLines: []int{36}},
	{ID: "q07", Title: "并行查询价格", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{63, 64, 65, 66, 67}, DistractLines: []int{32}},
	{ID: "q08", Title: "令牌桶限流", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{30, 31, 32, 33, 34, 35, 36, 37, 38, 39}, DistractLines: []int{29}},
	{ID: "q09", Title: "HTTP 用户服务客户端", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{42, 43, 44, 45, 46}, DistractLines: []int{51}},
	{ID: "q10", Title: "批量 CSV 导入", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{63, 64, 65, 66, 67}, DistractLines: []int{38}},
	{ID: "q11", Title: "文本报表写入", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{50, 51, 52, 53}, DistractLines: []int{24}},
	{ID: "q12", Title: "长度前缀消息解码", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{53, 54, 55, 56, 57, 68, 69, 70, 71, 72}, DistractLines: []int{45}},
	{ID: "q13", Title: "用户批量激活", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{29, 30, 31, 32, 33}, DistractLines: []int{18}},
	{ID: "q14", Title: "内存存储分页", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{53, 54, 55, 56, 57}, DistractLines: []int{43}},
	{ID: "q15", Title: "订单明细合并", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{34, 35, 36, 37, 38, 39}, DistractLines: []int{21}},
	{ID: "q16", Title: "注册参数校验", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{43, 44, 45, 46, 47}, DistractLines: []int{67}},
	{ID: "q17", Title: "乐观锁冲突重试", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{63, 64, 65, 66, 67}, DistractLines: []int{50}},
	{ID: "q18", Title: "事务批量保存", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{30, 31, 32, 33, 34}, DistractLines: []int{37}},
	{ID: "q19", Title: "支付扣款重试", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{46, 47, 48, 49, 50, 51, 52, 53, 54, 83, 84, 85, 86}, DistractLines: []int{44, 76}},
	{ID: "q20", Title: "订单分页查询", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{29, 30, 31, 32, 33}, DistractLines: []int{28}},
	{ID: "q21", Title: "LRU 缓存", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{47, 49, 50, 51}, DistractLines: []int{48}},
	{ID: "q22", Title: "会话续期（乐观并发）", Difficulty: "难", Weight: 2, Clean: false,
		HitLines: []int{58, 59, 60, 61, 62}, DistractLines: []int{75}},
	{ID: "q23", Title: "二进制协议解析", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{37, 38, 39, 40, 41}, DistractLines: []int{26}},
	{ID: "q24", Title: "熔断器", Difficulty: "很难", Weight: 3, Clean: false,
		HitLines: []int{54, 55, 56, 57, 58}, DistractLines: []int{39}},
	{ID: "q25", Title: "推送摘要截断", Difficulty: "中", Weight: 1, Clean: false,
		HitLines: []int{24, 25, 26, 27, 28}, DistractLines: []int{11}},
	{ID: "q26", Title: "日志目录统计（干净题）", Difficulty: "中", Weight: 1, Clean: true},
	{ID: "q27", Title: "配置懒加载（干净题）", Difficulty: "难", Weight: 2, Clean: true},
	{ID: "q28", Title: "用户批量激活（干净题）", Difficulty: "中", Weight: 1, Clean: true},
	{ID: "q29", Title: "搜索请求扇出（干净题）", Difficulty: "难", Weight: 2, Clean: true},
	{ID: "q30", Title: "注册参数校验（干净题）", Difficulty: "难", Weight: 2, Clean: true},
}

// questionCodes holds the Go source code for each question (key = "q01" etc.).
// These are the exact code blocks from modelprobe-question-bank.md, used verbatim
// in the user prompt with line numbers prepended.
var questionCodes = map[string]string{
	"q01": `package q01

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Result struct {
	URL  string
	Size int64
	Err  error
}

type Downloader struct {
	client *http.Client
	sem    chan struct{}
}

func NewDownloader(parallel int) *Downloader {
	if parallel <= 0 {
		parallel = 4
	}
	return &Downloader{
		client: &http.Client{Timeout: 30 * time.Second},
		sem:    make(chan struct{}, parallel),
	}
}

func (d *Downloader) fetch(url string) Result {
	resp, err := d.client.Get(url)
	if err != nil {
		return Result{URL: url, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{URL: url, Err: fmt.Errorf("unexpected status %d", resp.StatusCode)}
	}
	n, err := io.Copy(io.Discard, resp.Body)
	return Result{URL: url, Size: n, Err: err}
}

func (d *Downloader) FetchAll(urls []string) []Result {
	results := make([]Result, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		go func() {
			wg.Add(1)
			defer wg.Done()
			d.sem <- struct{}{}
			defer func() { <-d.sem }()
			results[i] = d.fetch(u)
		}()
	}
	wg.Wait()
	return results
}

func Summary(rs []Result) (ok, failed int, total int64) {
	for _, r := range rs {
		if r.Err != nil {
			failed++
			continue
		}
		ok++
		total += r.Size
	}
	return ok, failed, total
}`,

	"q02": `package q02

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Job struct {
	ID   int
	Text string
}

type Output struct {
	JobID    int
	Worker   int
	Words    int
	Duration time.Duration
}

func handle(worker int, j Job) Output {
	start := time.Now()
	words := len(strings.Fields(j.Text))
	return Output{JobID: j.ID, Worker: worker, Words: words, Duration: time.Since(start)}
}

type Pool struct {
	workers int
	stopped chan struct{}
	once    sync.Once
}

func NewPool(workers int) *Pool {
	return &Pool{workers: workers, stopped: make(chan struct{})}
}

func (p *Pool) Stop() {
	p.once.Do(func() { close(p.stopped) })
}

func (p *Pool) Run(jobs []Job) ([]Output, error) {
	if p.workers <= 0 {
		return nil, fmt.Errorf("invalid worker count %d", p.workers)
	}
	jobCh := make(chan Job)
	outCh := make(chan Output)

	for w := 0; w < p.workers; w++ {
		go func(id int) {
			for j := range jobCh {
				outCh <- handle(id, j)
			}
			close(outCh)
		}(w)
	}

	go func() {
		defer close(jobCh)
		for _, j := range jobs {
			select {
			case jobCh <- j:
			case <-p.stopped:
				return
			}
		}
	}()

	outs := make([]Output, 0, len(jobs))
	for o := range outCh {
		outs = append(outs, o)
	}
	return outs, nil
}`,

	"q03": `package q03

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
)

type Config struct {
	Endpoint string          ` + "`" + `json:"endpoint"` + "`" + `
	Timeout  int             ` + "`" + `json:"timeout_ms"` + "`" + `
	Features map[string]bool ` + "`" + `json:"features"` + "`" + `
}

type Loader struct {
	path   string
	mu     sync.Mutex
	loaded bool
	cfg    *Config
	err    error
	reads  atomic.Int64
}

func NewLoader(path string) *Loader {
	return &Loader{path: path}
}

func (l *Loader) load() (*Config, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Features == nil {
		c.Features = map[string]bool{}
	}
	return &c, nil
}

func (l *Loader) Get() (*Config, error) {
	l.reads.Add(1)
	if l.loaded {
		return l.cfg, l.err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		l.cfg, l.err = l.load()
		l.loaded = true
	}
	return l.cfg, l.err
}

func (l *Loader) Enabled(name string) bool {
	c, err := l.Get()
	if err != nil {
		return false
	}
	return c.Features[name]
}

func (l *Loader) Reads() int64 {
	return l.reads.Load()
}`,

	"q04": `package q04

import (
	"sync"
	"time"
)

type entry struct {
	val    string
	expire time.Time
}

type Cache struct {
	mu     sync.RWMutex
	items  map[string]entry
	ttl    time.Duration
	loader func(key string) (string, error)
}

func New(ttl time.Duration, loader func(string) (string, error)) *Cache {
	return &Cache{items: make(map[string]entry), ttl: ttl, loader: loader}
}

func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok || time.Now().After(e.expire) {
		return "", false
	}
	return e.val, true
}

func (c *Cache) Set(key, val string) {
	c.mu.Lock()
	c.items[key] = entry{val: val, expire: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *Cache) GetOrLoad(key string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.items[key]; ok && time.Now().Before(e.expire) {
		return e.val, nil
	}
	v, err := c.loader(key)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.items[key] = entry{val: v, expire: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return v, nil
}

func (c *Cache) Purge() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	n := 0
	for k, e := range c.items {
		if now.After(e.expire) {
			delete(c.items, k)
			n++
		}
	}
	return n
}`,

	"q05": `package q05

import (
	"context"
	"errors"
	"time"
)

var (
	ErrTimeout = errors.New("wait timeout")
	ErrFailed  = errors.New("job failed")
)

type StatusClient interface {
	Status(ctx context.Context, jobID string) (string, error)
}

type Waiter struct {
	client   StatusClient
	interval time.Duration
	maxErrs  int
}

func NewWaiter(c StatusClient) *Waiter {
	return &Waiter{client: c, interval: 500 * time.Millisecond, maxErrs: 5}
}

func (w *Waiter) WaitReady(ctx context.Context, jobID string, timeout time.Duration) error {
	tick := time.NewTicker(w.interval)
	defer tick.Stop()
	errs := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout):
			return ErrTimeout
		case <-tick.C:
			st, err := w.client.Status(ctx, jobID)
			if err != nil {
				errs++
				if errs >= w.maxErrs {
					return err
				}
				continue
			}
			errs = 0
			switch st {
			case "ready":
				return nil
			case "failed":
				return ErrFailed
			}
		}
	}
}`,

	"q06": `package q06

import (
	"context"
	"sort"
	"time"
)

type Item struct {
	ID    string
	Score float64
}

type Backend interface {
	Search(ctx context.Context, q string) ([]Item, error)
}

type result struct {
	items []Item
	err   error
}

type Aggregator struct {
	backends []Backend
	timeout  time.Duration
}

func NewAggregator(timeout time.Duration, bs ...Backend) *Aggregator {
	return &Aggregator{backends: bs, timeout: timeout}
}

func (a *Aggregator) Query(ctx context.Context, q string) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	ch := make(chan result)
	for _, b := range a.backends {
		go func() {
			items, err := b.Search(ctx, q)
			ch <- result{items: items, err: err}
		}()
	}
	var all []Item
	for range a.backends {
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			all = append(all, r.items...)
		case <-ctx.Done():
			return all, ctx.Err()
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	return all, nil
}`,

	"q07": `package q07

import (
	"context"
	"sync"
)

type Price struct {
	SKU   string
	Cents int64
}

type Stock struct {
	SKU string
	Qty int
}

type Fetcher interface {
	Price(ctx context.Context, sku string) (Price, error)
	Stock(ctx context.Context, sku string) (Stock, error)
}

func CollectStock(ctx context.Context, f Fetcher, skus []string) ([]Stock, error) {
	out := make([]Stock, len(skus))
	errs := make([]error, len(skus))
	var wg sync.WaitGroup
	for i, sku := range skus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = f.Stock(ctx, sku)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func CollectPrices(ctx context.Context, f Fetcher, skus []string) ([]Price, error) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		prices   []Price
	)
	for _, sku := range skus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.Price(ctx, sku)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			prices = append(prices, p)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return prices, nil
}`,

	"q08": `package q08

import (
	"sync"
	"time"
)

type Bucket struct {
	mu       sync.Mutex
	capacity int64
	tokens   int64
	perMin   int64
	last     time.Time
	now      func() time.Time
}

func NewBucket(capacity, perMin int64) *Bucket {
	b := &Bucket{capacity: capacity, tokens: capacity, perMin: perMin, now: time.Now}
	b.last = b.now()
	return b
}

func (b *Bucket) refill() {
	now := b.now()
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	add := int64(elapsed/time.Minute) * b.perMin
	b.tokens += add
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
}

func (b *Bucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

func (b *Bucket) Remaining() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.tokens
}`,

	"q09": `package q09

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

var ErrNotFound = errors.New("user not found")

type User struct {
	ID    string ` + "`" + `json:"id"` + "`" + `
	Name  string ` + "`" + `json:"name"` + "`" + `
	Email string ` + "`" + `json:"email"` + "`" + `
}

type Client struct {
	base  string
	token string
	http  *http.Client
}

func NewClient(base, token string) *Client {
	return &Client{base: base, token: token, http: &http.Client{}}
}

func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	u := c.base + "/users/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("get user %s: status %d: %s", id, resp.StatusCode, msg)
	}
	defer resp.Body.Close()
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}
	return &user, nil
}`,

	"q10": `package q10

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Importer struct {
	rows int
}

func (p *Importer) importReader(r io.Reader) (int, error) {
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
	}
	p.rows += n
	return n, sc.Err()
}

func (p *Importer) ImportFiles(paths []string) (int, error) {
	total := 0
	for _, path := range paths {
		n, err := func() (int, error) {
			f, err := os.Open(path)
			if err != nil {
				return 0, err
			}
			defer f.Close()
			return p.importReader(f)
		}()
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (p *Importer) ImportDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".csv") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return total, err
		}
		defer f.Close()
		n, err := p.importReader(f)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}`,

	"q11": `package q11

import (
	"bufio"
	"fmt"
	"os"
	"time"
)

type Row struct {
	ID     int64
	Name   string
	Amount float64
}

func AppendLog(path, msg string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "%s %s\n", time.Now().Format(time.RFC3339), msg)
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func WriteReport(path string, rows []Row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if _, err := fmt.Fprintf(w, "%-8s %-20s %12s\n", "ID", "NAME", "AMOUNT"); err != nil {
		return err
	}
	var sum float64
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "%-8d %-20s %12.2f\n", r.ID, r.Name, r.Amount); err != nil {
			return err
		}
		sum += r.Amount
	}
	if _, err := fmt.Fprintf(w, "%-29s %12.2f\n", "TOTAL", sum); err != nil {
		return err
	}
	return nil
}`,

	"q12": `package q12

import (
	"encoding/binary"
	"errors"
	"io"
)

var ErrTooLarge = errors.New("message too large")

const maxMsg = 64 * 1024

type Reader struct {
	r   io.Reader
	buf []byte
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: r, buf: make([]byte, maxMsg)}
}

func (rd *Reader) readFrame() (int, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(rd.r, hdr[:]); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxMsg {
		return 0, ErrTooLarge
	}
	if _, err := io.ReadFull(rd.r, rd.buf[:n]); err != nil {
		return 0, err
	}
	return int(n), nil
}

func (rd *Reader) NextCopy() ([]byte, error) {
	n, err := rd.readFrame()
	if err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, rd.buf[:n])
	return out, nil
}

func (rd *Reader) Next() ([]byte, error) {
	n, err := rd.readFrame()
	if err != nil {
		return nil, err
	}
	return rd.buf[:n], nil
}

func ReadAll(r io.Reader) ([][]byte, error) {
	rd := NewReader(r)
	var msgs [][]byte
	for {
		m, err := rd.Next()
		if err == io.EOF {
			return msgs, nil
		}
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
}`,

	"q13": `package q13

import "time"

type User struct {
	ID          int64
	Email       string
	Active      bool
	ActivatedAt time.Time
	ExpiresAt   time.Time
}

func DeactivateExpired(users []User, now time.Time) int {
	n := 0
	for i := range users {
		if users[i].Active && now.After(users[i].ExpiresAt) {
			users[i].Active = false
			n++
		}
	}
	return n
}

func ActivateAll(users []User, ids map[int64]bool, now time.Time) int {
	n := 0
	for _, u := range users {
		if ids[u.ID] && !u.Active {
			u.Active = true
			u.ActivatedAt = now
			n++
		}
	}
	return n
}

func ActiveEmails(users []User) []string {
	var out []string
	for _, u := range users {
		if u.Active {
			out = append(out, u.Email)
		}
	}
	return out
}`,

	"q14": `package q14

import (
	"sort"
	"sync"
)

type Item struct {
	ID    string
	Title string
	Tags  []string
}

type Store struct {
	mu    sync.RWMutex
	items map[string]Item
}

func NewStore() *Store {
	return &Store{items: make(map[string]Item)}
}

func (s *Store) Put(it Item) {
	s.mu.Lock()
	s.items[it.ID] = it
	s.mu.Unlock()
}

func (s *Store) Tags() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	for _, it := range s.items {
		for _, t := range it.Tags {
			seen[t] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (s *Store) List(cursor, limit int) ([]Item, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, 0, limit)
	i := 0
	for _, it := range s.items {
		if i >= cursor && len(out) < limit {
			out = append(out, it)
		}
		i++
	}
	next := cursor + len(out)
	if next >= len(s.items) {
		next = -1
	}
	return out, next
}`,

	"q15": `package q15

import "errors"

var ErrQty = errors.New("quantity must be positive")

type Line struct {
	SKU   string
	Qty   int
	Price int64
}

type Order struct {
	Lines []Line
	bySKU map[string]*Line
}

func NewOrder() *Order {
	return &Order{
		Lines: make([]Line, 0, 4),
		bySKU: make(map[string]*Line),
	}
}

func (o *Order) Add(sku string, qty int, price int64) (*Line, error) {
	if qty <= 0 {
		return nil, ErrQty
	}
	if l, ok := o.bySKU[sku]; ok {
		l.Qty += qty
		return l, nil
	}
	o.Lines = append(o.Lines, Line{SKU: sku, Qty: qty, Price: price})
	l := &o.Lines[len(o.Lines)-1]
	o.bySKU[sku] = l
	return l, nil
}

func (o *Order) Total() int64 {
	var sum int64
	for _, l := range o.Lines {
		sum += int64(l.Qty) * l.Price
	}
	return sum
}

func (o *Order) Count() int {
	return len(o.Lines)
}`,

	"q16": `package q16

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

type SignupReq struct {
	Email    string
	Password string
	Nickname string
}

type Service struct {
	mu    sync.Mutex
	users map[string]SignupReq
}

func NewService() *Service {
	return &Service{users: make(map[string]SignupReq)}
}

func (s *Service) check(r *SignupReq) error {
	var verr *ValidationError
	switch {
	case !strings.Contains(r.Email, "@"):
		verr = &ValidationError{Field: "email", Msg: "invalid"}
	case len(r.Password) < 8:
		verr = &ValidationError{Field: "password", Msg: "too short"}
	case len(r.Nickname) > 32:
		verr = &ValidationError{Field: "nickname", Msg: "too long"}
	}
	return verr
}

func (s *Service) Signup(r *SignupReq) error {
	if err := s.check(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[r.Email]; ok {
		return errors.New("email already registered")
	}
	s.users[r.Email] = *r
	return nil
}

func (s *Service) Handle(w http.ResponseWriter, r *SignupReq) {
	err := s.Signup(r)
	var verr *ValidationError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusCreated)
	case errors.As(err, &verr):
		http.Error(w, verr.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusConflict)
	}
}`,

	"q17": `package q17

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrConflict = errors.New("version conflict")
	ErrNotFound = errors.New("not found")
)

type Doc struct {
	ID      string
	Version int
	Body    string
}

type Repo interface {
	Get(ctx context.Context, id string) (Doc, error)
	Update(ctx context.Context, d Doc) error
}

type memRepo struct{ docs map[string]Doc }

func (m *memRepo) Get(_ context.Context, id string) (Doc, error) {
	d, ok := m.docs[id]
	if !ok {
		return Doc{}, fmt.Errorf("get %s: %w", id, ErrNotFound)
	}
	return d, nil
}

func (m *memRepo) Update(_ context.Context, d Doc) error {
	cur, ok := m.docs[d.ID]
	if !ok {
		return fmt.Errorf("update %s: %w", d.ID, ErrNotFound)
	}
	if cur.Version != d.Version {
		return fmt.Errorf("update %s: %w", d.ID, ErrConflict)
	}
	d.Version++
	m.docs[d.ID] = d
	return nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func Append(ctx context.Context, r Repo, id, text string) error {
	for attempt := 0; attempt < 3; attempt++ {
		d, err := r.Get(ctx, id)
		if err != nil {
			return err
		}
		d.Body += text
		err = r.Update(ctx, d)
		if err == nil {
			return nil
		}
		if err == ErrConflict {
			continue
		}
		return err
	}
	return ErrConflict
}`,

	"q18": `package q18

import (
	"context"
	"database/sql"
)

type Item struct {
	ID    int64
	Name  string
	Price int64
}

type Store struct {
	db *sql.DB
}

const (
	insertSQL = ` + "`" + `INSERT INTO items (name, price) VALUES (?, ?)` + "`" + `
	updateSQL = ` + "`" + `UPDATE items SET name = ?, price = ? WHERE id = ?` + "`" + `
)

func (s *Store) SaveAll(ctx context.Context, items []Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.ID == 0 {
			_, err := tx.ExecContext(ctx, insertSQL, it.Name, it.Price)
			if err != nil {
				break
			}
		} else {
			_, err = tx.ExecContext(ctx, updateSQL, it.Name, it.Price, it.ID)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, ` + "`" + `SELECT COUNT(*) FROM items` + "`" + `).Scan(&n)
	return n, err
}`,

	"q19": `package q19

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type ChargeResult struct {
	ID     string ` + "`" + `json:"id"` + "`" + `
	Status string ` + "`" + `json:"status"` + "`" + `
}

type chargeReq struct {
	OrderID string ` + "`" + `json:"order_id"` + "`" + `
	Amount  int64  ` + "`" + `json:"amount"` + "`" + `
}

type Client struct {
	http     *http.Client
	base     string
	maxRetry int
}

func NewClient(base string) *Client {
	return &Client{http: &http.Client{Timeout: 5 * time.Second}, base: base, maxRetry: 2}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (c *Client) send(ctx context.Context, method, path string, body []byte) (*ChargeResult, error) {
	var lastErr error
	for i := 0; i <= c.maxRetry; i++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if isTimeout(err) {
				continue
			}
			return nil, err
		}
		return decode(resp)
	}
	return nil, fmt.Errorf("after %d attempts: %w", c.maxRetry+1, lastErr)
}

func decode(resp *http.Response) (*ChargeResult, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider status %d", resp.StatusCode)
	}
	var r ChargeResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) GetCharge(ctx context.Context, id string) (*ChargeResult, error) {
	return c.send(ctx, http.MethodGet, "/v1/charges/"+id, nil)
}

func (c *Client) Charge(ctx context.Context, orderID string, amount int64) (*ChargeResult, error) {
	body, err := json.Marshal(chargeReq{OrderID: orderID, Amount: amount})
	if err != nil {
		return nil, err
	}
	return c.send(ctx, http.MethodPost, "/v1/charges", body)
}`,

	"q20": `package q20

import (
	"context"
	"database/sql"
	"time"
)

type Order struct {
	ID        int64
	UserID    int64
	Amount    int64
	CreatedAt time.Time
}

type Repo struct {
	db *sql.DB
}

const listSQL = ` + "`" + `SELECT id, user_id, amount, created_at FROM orders
WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?` + "`" + `

func (r *Repo) ListOrders(ctx context.Context, userID int64, page, size int) ([]Order, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	offset := page * size
	rows, err := r.db.QueryContext(ctx, listSQL, userID, size, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Order, 0, size)
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.Amount, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}`,

	"q21": `package q21

import (
	"container/list"
	"sync"
)

type entry struct {
	key string
	val []byte
}

type LRU struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	idx map[string]*list.Element
}

func NewLRU(capacity int) *LRU {
	return &LRU{max: capacity, ll: list.New(), idx: make(map[string]*list.Element)}
}

func (c *LRU) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.idx[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry).val, true
}

func (c *LRU) Put(key string, val []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		el.Value.(*entry).val = val
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&entry{key: key, val: val})
	c.idx[key] = el
	if c.ll.Len() > c.max {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.idx, key)
	}
}

func (c *LRU) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}`,

	"q22": `package q22

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var ErrNoSession = errors.New("session not found")

type Session struct {
	ID        string    ` + "`" + `json:"id"` + "`" + `
	UserID    int64     ` + "`" + `json:"user_id"` + "`" + `
	ExpiresAt time.Time ` + "`" + `json:"expires_at"` + "`" + `
}

type Store struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewStore() *Store { return &Store{data: make(map[string][]byte)} }

func (s *Store) load(id string) (Session, error) {
	raw, ok := s.data[id]
	if !ok {
		return Session{}, ErrNoSession
	}
	var sess Session
	err := json.Unmarshal(raw, &sess)
	return sess, err
}

func (s *Store) Create(id string, userID int64, ttl time.Duration) (Session, error) {
	sess := Session{ID: id, UserID: userID, ExpiresAt: time.Now().Add(ttl)}
	raw, err := json.Marshal(sess)
	if err != nil {
		return Session{}, err
	}
	s.mu.Lock()
	s.data[id] = raw
	s.mu.Unlock()
	return sess, nil
}

func (s *Store) Extend(prev Session, ttl time.Duration) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load(prev.ID)
	if err != nil {
		return Session{}, false, err
	}
	if cur.ExpiresAt != prev.ExpiresAt {
		return cur, false, nil
	}
	next := cur
	next.ExpiresAt = time.Now().Add(ttl)
	raw, err := json.Marshal(next)
	if err != nil {
		return Session{}, false, err
	}
	s.data[next.ID] = raw
	return next, true, nil
}

func (s Session) Expired(now time.Time) bool {
	return now.After(s.ExpiresAt)
}`,

	"q23": `package q23

import (
	"encoding/binary"
	"errors"
)

var (
	ErrShort  = errors.New("record too short")
	ErrBounds = errors.New("payload out of bounds")
	ErrType   = errors.New("unknown record type")
)

const headerLen = 12

type Record struct {
	Type    uint16
	Flags   uint16
	Payload []byte
}

func Parse(buf []byte) (Record, error) {
	if len(buf) < headerLen {
		return Record{}, ErrShort
	}
	typ := binary.BigEndian.Uint16(buf[0:2])
	if typ == 0 || typ > 8 {
		return Record{}, ErrType
	}
	flags := binary.BigEndian.Uint16(buf[2:4])
	off := binary.BigEndian.Uint32(buf[4:8])
	n := binary.BigEndian.Uint32(buf[8:12])
	if off < headerLen {
		return Record{}, ErrBounds
	}
	if off+n > uint32(len(buf)) {
		return Record{}, ErrBounds
	}
	return Record{Type: typ, Flags: flags, Payload: buf[off : off+n]}, nil
}

func ParseAll(packets [][]byte) ([]Record, []error) {
	var recs []Record
	var errs []error
	for _, p := range packets {
		r, err := Parse(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs = append(recs, r)
	}
	return recs, errs
}`,

	"q24": `package q24

import (
	"errors"
	"sync"
	"time"
)

var ErrOpen = errors.New("circuit open")

type state int

const (
	closed state = iota
	open
	halfOpen
)

type Breaker struct {
	mu        sync.Mutex
	Threshold int
	Cooldown  time.Duration
	st        state
	failures  int
	openedAt  time.Time
	trial     bool
}

func (b *Breaker) allow(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.st {
	case open:
		if now.Sub(b.openedAt) < b.Cooldown {
			return ErrOpen
		}
		b.st = halfOpen
		b.trial = true
		return nil
	case halfOpen:
		if b.trial {
			return ErrOpen
		}
		b.trial = true
		return nil
	}
	return nil
}

func (b *Breaker) record(now time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		if b.st == halfOpen {
			b.st = closed
			b.failures = 0
		}
		b.trial = false
		return
	}
	b.failures++
	if b.st == halfOpen || b.failures >= b.Threshold {
		b.st = open
		b.openedAt = now
	}
	b.trial = false
}

func (b *Breaker) Do(fn func() error) error {
	if err := b.allow(time.Now()); err != nil {
		return err
	}
	err := fn()
	b.record(time.Now(), err)
	return err
}`,

	"q25": `package q25

import (
	"strings"
	"unicode/utf8"
)

func TruncateTitle(title string, max int) string {
	r := []rune(title)
	if len(r) <= max {
		return title
	}
	return string(r[:max]) + "…"
}

func Summary(body string, max int) string {
	body = strings.TrimSpace(body)
	body = strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(body) <= max {
		return body
	}
	return body[:max] + "…"
}

func WordCount(body string) int {
	return len(strings.Fields(body))
}`,

	"q26": `package q26

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type Stats struct {
	Files int
	Lines int
	Bytes int64
}

func ScanDir(dir string) (Stats, error) {
	var st Stats
	entries, err := os.ReadDir(dir)
	if err != nil {
		return st, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		err := func() error {
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			st.Bytes += info.Size()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				if strings.TrimSpace(sc.Text()) != "" {
					st.Lines++
				}
			}
			return sc.Err()
		}()
		if err != nil {
			return st, err
		}
		st.Files++
	}
	return st, nil
}`,

	"q27": `package q27

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
)

type Config struct {
	Endpoint string          ` + "`" + `json:"endpoint"` + "`" + `
	Timeout  int             ` + "`" + `json:"timeout_ms"` + "`" + `
	Features map[string]bool ` + "`" + `json:"features"` + "`" + `
}

type Loader struct {
	path  string
	once  sync.Once
	cfg   *Config
	err   error
	reads atomic.Int64
}

func NewLoader(path string) *Loader {
	return &Loader{path: path}
}

func (l *Loader) load() {
	data, err := os.ReadFile(l.path)
	if err != nil {
		l.err = err
		return
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		l.err = err
		return
	}
	if c.Features == nil {
		c.Features = map[string]bool{}
	}
	l.cfg = &c
}

func (l *Loader) Get() (*Config, error) {
	l.reads.Add(1)
	l.once.Do(l.load)
	return l.cfg, l.err
}

func (l *Loader) Enabled(name string) bool {
	c, err := l.Get()
	if err != nil {
		return false
	}
	return c.Features[name]
}

func (l *Loader) Reads() int64 {
	return l.reads.Load()
}`,

	"q28": `package q28

import "time"

type User struct {
	ID          int64
	Email       string
	Active      bool
	ActivatedAt time.Time
	ExpiresAt   time.Time
}

func ActivateAll(users []User, ids map[int64]bool, now time.Time) int {
	n := 0
	for i := range users {
		u := &users[i]
		if ids[u.ID] && !u.Active {
			u.Active = true
			u.ActivatedAt = now
			n++
		}
	}
	return n
}

func ActiveEmails(users []User) []string {
	var out []string
	for _, u := range users {
		if u.Active {
			out = append(out, u.Email)
		}
	}
	return out
}

func Snapshot(users []User) []User {
	out := make([]User, len(users))
	copy(out, users)
	return out
}

func ExpiringSoon(users []User, now time.Time, d time.Duration) []User {
	var out []User
	for _, u := range users {
		if u.Active && u.ExpiresAt.After(now) && u.ExpiresAt.Sub(now) <= d {
			out = append(out, u)
		}
	}
	return out
}`,

	"q29": `package q29

import (
	"context"
	"sort"
	"time"
)

type Item struct {
	ID    string
	Score float64
}

type Backend interface {
	Search(ctx context.Context, q string) ([]Item, error)
}

type result struct {
	items []Item
	err   error
}

type Aggregator struct {
	backends []Backend
	timeout  time.Duration
}

func NewAggregator(timeout time.Duration, bs ...Backend) *Aggregator {
	return &Aggregator{backends: bs, timeout: timeout}
}

func (a *Aggregator) Query(ctx context.Context, q string) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	ch := make(chan result, len(a.backends))
	for _, b := range a.backends {
		go func() {
			items, err := b.Search(ctx, q)
			ch <- result{items: items, err: err}
		}()
	}
	var all []Item
	for range a.backends {
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			all = append(all, r.items...)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	return all, nil
}`,

	"q30": `package q30

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

type SignupReq struct {
	Email    string
	Password string
	Nickname string
}

type Service struct {
	mu    sync.Mutex
	users map[string]SignupReq
}

func NewService() *Service {
	return &Service{users: make(map[string]SignupReq)}
}

func (s *Service) check(r *SignupReq) error {
	switch {
	case !strings.Contains(r.Email, "@"):
		return &ValidationError{Field: "email", Msg: "invalid"}
	case len(r.Password) < 8:
		return &ValidationError{Field: "password", Msg: "too short"}
	case len(r.Nickname) > 32:
		return &ValidationError{Field: "nickname", Msg: "too long"}
	}
	return nil
}

func (s *Service) Signup(r *SignupReq) error {
	if err := s.check(r); err != nil {
		return fmt.Errorf("signup: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[r.Email]; ok {
		return errors.New("email already registered")
	}
	s.users[r.Email] = *r
	return nil
}

func (s *Service) Handle(w http.ResponseWriter, r *SignupReq) {
	err := s.Signup(r)
	var verr *ValidationError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusCreated)
	case errors.As(err, &verr):
		http.Error(w, verr.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusConflict)
	}
}`,
}

// ──────────────────────────────────────────────────────────────────
// Prompt builder
// ──────────────────────────────────────────────────────────────────

// formatCode adds right-aligned line numbers to a Go source block.
// Line numbers start at 1, right-aligned in 3 characters followed by "|".
func formatCode(src string) string {
	lines := strings.Split(src, "\n")
	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("%3d| %s\n", i+1, line))
	}
	return sb.String()
}

// buildPrompt constructs the user message for a single question.
func buildPrompt(qid, requestID string) string {
	code := questionCodes[qid]
	numbered := formatCode(code)
	return fmt.Sprintf(
		"请审查下面的 Go 代码（Go 1.22 及以上版本）。\n"+
			"代码中最多有 1 个会导致错误行为的缺陷，也可能没有。\n"+
			"代码风格、命名、性能优化建议、未处理 Close 返回值这类问题不算缺陷。\n"+
			"只输出一行 JSON，不要任何解释：\n"+
			`{"bug": true, "line": 行号, "reason": "一句话原因"}`+"\n"+
			"没有缺陷时输出：{\"bug\": false}\n\n"+
			"%s",
		numbered,
	)
}

// buildSystem returns the system prompt with a unique ref.
func buildSystem(requestID string) string {
	return fmt.Sprintf("你是一名资深 Go 工程师，负责代码审查。ref: %s", requestID)
}

// ──────────────────────────────────────────────────────────────────
// API types
// ──────────────────────────────────────────────────────────────────

type msgMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type msgRequest struct {
	Model       string       `json:"model"`
	MaxTokens   int          `json:"max_tokens"`
	System      string       `json:"system,omitempty"`
	Messages    []msgMessage `json:"messages"`
	Temperature *float64     `json:"temperature,omitempty"`
}

// bedrockRequest adds anthropic_version required by Bedrock.
type bedrockRequest struct {
	AnthropicVersion string       `json:"anthropic_version"`
	Model            string       `json:"model,omitempty"`
	MaxTokens        int          `json:"max_tokens"`
	System           string       `json:"system,omitempty"`
	Messages         []msgMessage `json:"messages"`
	Temperature      *float64     `json:"temperature,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type msgResponse struct {
	Type       string         `json:"type"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
}

type modelAnswer struct {
	Bug    bool   `json:"bug"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// ──────────────────────────────────────────────────────────────────
// Single-question result
// ──────────────────────────────────────────────────────────────────

type questionResult struct {
	QID         string
	Score       int  // 0 or 1
	Dropped     bool // true = HTTP error after retries; excluded from S_max
	RawResponse string
	ParseErr    bool // JSON parse failed
	DistractHit bool // answered a distract line
	MissedBug   bool // clean=false but answered bug:false
}

// score scores a single question result against the bank metadata.
func (r *questionResult) compute(q QuestionMeta, ans modelAnswer) {
	if q.Clean {
		if !ans.Bug {
			r.Score = 1
		} else {
			// clean question but model said bug=true
		}
		return
	}
	// buggy question
	if !ans.Bug {
		r.MissedBug = true
		return
	}
	// check distract lines first (penalised)
	for _, d := range q.DistractLines {
		if ans.Line == d {
			r.DistractHit = true
			return
		}
	}
	// check hit lines
	for _, h := range q.HitLines {
		if ans.Line == h {
			r.Score = 1
			return
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// Run result for one (channel, model) pair
// ──────────────────────────────────────────────────────────────────

type runResult struct {
	Channel string // "lianxiang-01", "aws", etc.
	Model   string
	Results []questionResult

	S    int     // weighted score
	SMax int     // max possible weighted score (adjusted for drops)
	P    float64 // S/S_max * 100
	R    float64 // S / S_control * 100 (set after control known)

	// per-difficulty
	ScoreByDiff map[string][2]int // diff -> [scored, max_count]
	CleanScore  [2]int            // [correct, total]

	// diagnostics
	MissRate      float64 // miss rate
	DistractRate  float64 // distract rate
	FormatErrRate float64

	// temperature info
	TempOmitted bool // temperature was not sent (backend rejected it)
}

func (r *runResult) compute() {
	diffWeight := map[string]int{"中": 1, "难": 2, "很难": 3}
	r.ScoreByDiff = map[string][2]int{}

	totalBuggy := 0
	missCount := 0
	distractCount := 0
	formatCount := 0

	for i, qr := range r.Results {
		if qr.Dropped {
			continue
		}
		q := questionBank[i]
		w := diffWeight[q.Difficulty]
		r.S += qr.Score * w
		r.SMax += w

		if q.Clean {
			cur := r.CleanScore
			cur[1]++
			cur[0] += qr.Score
			r.CleanScore = cur
		} else {
			cur := r.ScoreByDiff[q.Difficulty]
			cur[1] += w
			cur[0] += qr.Score * w
			r.ScoreByDiff[q.Difficulty] = cur
			totalBuggy++
			if qr.MissedBug {
				missCount++
			}
			if qr.DistractHit {
				distractCount++
			}
		}
		if qr.ParseErr {
			formatCount++
		}
	}

	if r.SMax > 0 {
		r.P = float64(r.S) / float64(r.SMax) * 100
	}
	total := 30
	// count dropped
	dropped := 0
	for _, qr := range r.Results {
		if qr.Dropped {
			dropped++
		}
	}
	total -= dropped

	if totalBuggy > 0 {
		r.MissRate = float64(missCount) / float64(totalBuggy) * 100
	}
	distractTotal := 30 - dropped
	if distractTotal > 0 {
		r.DistractRate = float64(distractCount+countCleanFalsePositives(r.Results)) / float64(distractTotal) * 100
	}
	if total > 0 {
		r.FormatErrRate = float64(formatCount) / float64(total) * 100
	}
}

func countCleanFalsePositives(results []questionResult) int {
	n := 0
	for i, qr := range results {
		if qr.Dropped {
			continue
		}
		q := questionBank[i]
		if q.Clean {
			ans := modelAnswer{}
			if err := json.Unmarshal([]byte(qr.RawResponse), &ans); err == nil {
				if ans.Bug {
					n++
				}
			}
		}
	}
	return n
}

// ──────────────────────────────────────────────────────────────────
// Persistence: save results to a daily JSON file
// ──────────────────────────────────────────────────────────────────

// runResultJSON is the JSON-serializable summary of one (channel, model) run.
// Only aggregate metrics are kept — per-question RawResponse is omitted to
// keep the saved file small.
type runResultJSON struct {
	Channel       string            `json:"channel"`
	Model         string            `json:"model"`
	S             int               `json:"s"`
	SMax          int               `json:"s_max"`
	P             float64           `json:"p"`
	R             float64           `json:"r"`
	MissRate      float64           `json:"miss_rate"`
	DistractRate  float64           `json:"distract_rate"`
	FormatErrRate float64           `json:"format_err_rate"`
	TempOmitted   bool              `json:"temp_omitted"`
	Judgment      string            `json:"judgment"`
	ScoreByDiff   map[string][2]int `json:"score_by_diff"`
	CleanScore    [2]int            `json:"clean_score"`
}

// probeRecord is one complete modelprobe run, saved to the daily JSON file.
type probeRecord struct {
	RunAt       string                              `json:"run_at"`
	ConfigPath  string                              `json:"config_path"`
	Models      []string                            `json:"models"`
	Backends    []string                            `json:"backends"`
	TimeoutSec  int                                 `json:"timeout_sec"`
	Concurrency int                                 `json:"concurrency"`
	ControlRuns map[string]runResultJSON            `json:"control_runs"`
	BackendRuns map[string]map[string]runResultJSON `json:"backend_runs"`
}

// toJSON converts a runResult to its JSON-serializable summary.
func (r *runResult) toJSON() runResultJSON {
	return runResultJSON{
		Channel:       r.Channel,
		Model:         r.Model,
		S:             r.S,
		SMax:          r.SMax,
		P:             r.P,
		R:             r.R,
		MissRate:      r.MissRate,
		DistractRate:  r.DistractRate,
		FormatErrRate: r.FormatErrRate,
		TempOmitted:   r.TempOmitted,
		Judgment:      judgment(r.R),
		ScoreByDiff:   r.ScoreByDiff,
		CleanScore:    r.CleanScore,
	}
}

// saveResults appends rec to <logDir>/modelprobe-YYYY-MM-DD.json as one
// entry in a JSON array (one array element per run on that day). Writes are
// atomic via a temp file + rename, matching the pattern used for the ipgeo
// cache file.
func saveResults(logDir string, rec probeRecord) (string, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}
	date := time.Now().Format("2006-01-02")
	path := filepath.Join(logDir, fmt.Sprintf("modelprobe-%s.json", date))

	// Load existing records for today (if any); tolerate a missing or
	// unreadable file by starting a fresh array.
	var records []json.RawMessage
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &records)
	}

	newEntry, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("marshal record: %w", err)
	}
	records = append(records, json.RawMessage(newEntry))

	out, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal array: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("rename temp file: %w", err)
	}
	return path, nil
}

// ──────────────────────────────────────────────────────────────────
// Caller: backend (plain HTTP)
// ──────────────────────────────────────────────────────────────────

type backendCaller struct {
	name   string
	url    string
	apiKey string
	client *http.Client
	noTemp bool // set to true if backend rejected temperature
	mu     sync.Mutex
}

// call sends one question to the backend channel. Returns raw JSON text response.
// It handles temperature rejection by retrying without temperature.
func (b *backendCaller) call(ctx context.Context, model, system, userMsg string) (string, error) {
	zero := 0.0
	var temp *float64
	b.mu.Lock()
	omitTemp := b.noTemp
	b.mu.Unlock()
	if !omitTemp {
		temp = &zero
	}

	req := msgRequest{
		Model:       model,
		MaxTokens:   200,
		System:      system,
		Messages:    []msgMessage{{Role: "user", Content: userMsg}},
		Temperature: temp,
	}

	raw, statusCode, respBody, err := b.doRequest(ctx, req)
	if err != nil {
		return "", err
	}

	// Detect temperature rejection (some backends return 400/422 with "temperature" in body)
	if (statusCode == 400 || statusCode == 422) && temp != nil &&
		strings.Contains(strings.ToLower(string(respBody)), "temperature") {
		b.mu.Lock()
		b.noTemp = true
		b.mu.Unlock()
		req.Temperature = nil
		raw, _, _, err = b.doRequest(ctx, req)
		if err != nil {
			return "", err
		}
	}

	return raw, nil
}

func (b *backendCaller) doRequest(ctx context.Context, req msgRequest) (string, int, []byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", 0, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", 0, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)
	httpReq.Header.Set("x-api-key", b.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return "", 0, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", resp.StatusCode, nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, respBody, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse the response to extract text
	var msgResp msgResponse
	if err := json.Unmarshal(respBody, &msgResp); err != nil {
		return "", resp.StatusCode, respBody, fmt.Errorf("parse response: %w", err)
	}
	if len(msgResp.Content) == 0 {
		return "", resp.StatusCode, respBody, fmt.Errorf("empty content in response")
	}
	return msgResp.Content[0].Text, resp.StatusCode, respBody, nil
}

// ──────────────────────────────────────────────────────────────────
// Caller: AWS Bedrock
// ──────────────────────────────────────────────────────────────────

type awsCaller struct {
	client *awsproxy.BedrockClient
	cfg    *config.AWSConfig
	noTemp bool
	mu     sync.Mutex
}

func (a *awsCaller) call(ctx context.Context, model, system, userMsg string) (string, error) {
	arn, err := awsproxy.ResolveModel(model, a.cfg.ModelReplace, a.cfg.ModelDefault)
	if err != nil {
		return "", fmt.Errorf("resolve model %q for aws: %w", model, err)
	}

	zero := 0.0
	var temp *float64
	a.mu.Lock()
	omitTemp := a.noTemp
	a.mu.Unlock()
	if !omitTemp {
		temp = &zero
	}

	body, err := buildBedrockBody(system, userMsg, temp)
	if err != nil {
		return "", err
	}

	respBytes, err := a.client.InvokeModel(ctx, arn, body)
	if err != nil {
		// Check if error is temperature-related
		if temp != nil && strings.Contains(strings.ToLower(err.Error()), "temperature") {
			a.mu.Lock()
			a.noTemp = true
			a.mu.Unlock()
			body, _ = buildBedrockBody(system, userMsg, nil)
			respBytes, err = a.client.InvokeModel(ctx, arn, body)
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}

	// Bedrock returns Anthropic Messages format
	var msgResp msgResponse
	if err := json.Unmarshal(respBytes, &msgResp); err != nil {
		return "", fmt.Errorf("parse bedrock response: %w", err)
	}
	if len(msgResp.Content) == 0 {
		return "", fmt.Errorf("empty content in bedrock response: %s", string(respBytes))
	}
	// Concatenate all text blocks (skip non-text blocks like thinking/redacted_thinking).
	var sb strings.Builder
	for _, blk := range msgResp.Content {
		if blk.Type == "" || blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	text := sb.String()
	if text == "" {
		return "", fmt.Errorf("no text content in bedrock response (stop_reason=%s): %s", msgResp.StopReason, string(respBytes))
	}
	return text, nil
}

func buildBedrockBody(system, userMsg string, temp *float64) ([]byte, error) {
	req := bedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        2000, // thinking models (sonnet-5/opus-5) consume thinking tokens; leave room for text
		System:           system,
		Messages:         []msgMessage{{Role: "user", Content: userMsg}},
		Temperature:      temp,
	}
	return json.Marshal(req)
}

// ──────────────────────────────────────────────────────────────────
// Question runner — runs all 30 questions against one caller
// ──────────────────────────────────────────────────────────────────

type caller interface {
	call(ctx context.Context, model, system, userMsg string) (string, error)
}

func runQuestions(ctx context.Context, c caller, model string, concurrency int, verbose bool, channelName string) []questionResult {
	results := make([]questionResult, len(questionBank))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, q := range questionBank {
		wg.Add(1)
		go func(idx int, q QuestionMeta) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			qr := questionResult{QID: q.ID}
			requestID := fmt.Sprintf("%s-%s-%s", channelName, model, q.ID)
			system := buildSystem(requestID)
			userMsg := buildPrompt(q.ID, requestID)

			var rawText string
			var lastErr error

			for attempt := 0; attempt < 3; attempt++ {
				rawText, lastErr = c.call(ctx, model, system, userMsg)
				if lastErr == nil {
					break
				}
				if verbose {
					fmt.Printf("  [%s/%s/%s] attempt %d error: %v\n", channelName, model, q.ID, attempt+1, lastErr)
				}
				// Brief backoff between retries
				select {
				case <-ctx.Done():
					qr.Dropped = true
					results[idx] = qr
					return
				case <-time.After(2 * time.Second):
				}
			}

			if lastErr != nil {
				qr.Dropped = true
				results[idx] = qr
				return
			}

			// Parse the JSON answer (model should output a single JSON line)
			qr.RawResponse = strings.TrimSpace(rawText)
			// Find the JSON object in the response (model may output extra whitespace/newlines)
			jsonStart := strings.Index(qr.RawResponse, "{")
			jsonEnd := strings.LastIndex(qr.RawResponse, "}")
			var ans modelAnswer
			if jsonStart == -1 || jsonEnd == -1 || jsonStart > jsonEnd {
				qr.ParseErr = true
			} else {
				jsonStr := qr.RawResponse[jsonStart : jsonEnd+1]
				if err := json.Unmarshal([]byte(jsonStr), &ans); err != nil {
					qr.ParseErr = true
				}
			}

			if !qr.ParseErr {
				qr.compute(q, ans)
			}

			if verbose {
				verdict := "✗"
				if qr.Score == 1 {
					verdict = "✓"
				}
				if qr.Dropped {
					verdict = "⊘"
				}
				fmt.Printf("  [%s/%s] %s %s  %s\n", channelName, model, q.ID, verdict, qr.RawResponse)
			}

			results[idx] = qr
		}(i, q)
	}

	wg.Wait()
	return results
}

// ──────────────────────────────────────────────────────────────────
// Judgment per § 3.7
// ──────────────────────────────────────────────────────────────────

func judgment(r float64) string {
	switch {
	case math.IsNaN(r) || r == 0:
		return "N/A"
	case r >= 90:
		return "正常"
	case r >= 80:
		return "关注"
	default:
		return "可疑"
	}
}

// ──────────────────────────────────────────────────────────────────
// Output printer
// ──────────────────────────────────────────────────────────────────

func printResults(models []string, backendNames []string, controlRuns map[string]*runResult, backendRuns map[string]map[string]*runResult) {
	// Column width: wide enough for the longest name + 2 padding
	w := len("aws (control)") + 2
	for _, bn := range backendNames {
		if len(bn)+2 > w {
			w = len(bn) + 2
		}
	}
	sepWidth := 22 + w*len(backendNames) + w // label col + data cols
	if sepWidth < 72 {
		sepWidth = 72
	}
	sep := strings.Repeat("═", sepWidth)
	thin := strings.Repeat("─", sepWidth)

	fmt.Printf("\n%s\n", sep)

	for _, model := range models {
		ctrl := controlRuns[model]
		fmt.Printf("Model: %s\n", model)
		fmt.Printf("%s\n", thin)

		// Build column headers
		headers := []string{""}
		for _, bn := range backendNames {
			headers = append(headers, bn)
		}
		headers = append(headers, "aws (control)")
		printRow(headers, w)
		fmt.Printf("%s\n", thin)

		// S row
		sVals := []string{"S"}
		for _, bn := range backendNames {
			r := backendRuns[bn][model]
			if r != nil {
				sVals = append(sVals, fmt.Sprintf("%d", r.S))
			} else {
				sVals = append(sVals, "—")
			}
		}
		sVals = append(sVals, fmt.Sprintf("%d", ctrl.S))
		printRow(sVals, w)

		// S_max row
		smVals := []string{"S_max"}
		for _, bn := range backendNames {
			r := backendRuns[bn][model]
			if r != nil {
				smVals = append(smVals, fmt.Sprintf("%d", r.SMax))
			} else {
				smVals = append(smVals, "—")
			}
		}
		smVals = append(smVals, fmt.Sprintf("%d", ctrl.SMax))
		printRow(smVals, w)

		// P row
		pVals := []string{"P"}
		for _, bn := range backendNames {
			r := backendRuns[bn][model]
			if r != nil {
				pVals = append(pVals, fmt.Sprintf("%.1f%%", r.P))
			} else {
				pVals = append(pVals, "—")
			}
		}
		pVals = append(pVals, fmt.Sprintf("%.1f%%", ctrl.P))
		printRow(pVals, w)

		// R row
		rVals := []string{"R vs aws"}
		for _, bn := range backendNames {
			r := backendRuns[bn][model]
			if r != nil && ctrl.S > 0 {
				r.R = float64(r.S) / float64(ctrl.S) * 100
				rVals = append(rVals, fmt.Sprintf("%.1f%%", r.R))
			} else {
				rVals = append(rVals, "—")
			}
		}
		rVals = append(rVals, "100%")
		printRow(rVals, w)

		fmt.Printf("%s\n", thin)

		// Per-difficulty
		fmt.Println("Per-difficulty (weighted score / max):")
		for _, diff := range []string{"中", "难", "很难"} {
			label := fmt.Sprintf("  %s (×%d)", diff, map[string]int{"中": 1, "难": 2, "很难": 3}[diff])
			vals := []string{label}
			for _, bn := range backendNames {
				r := backendRuns[bn][model]
				if r != nil {
					s := r.ScoreByDiff[diff]
					vals = append(vals, fmt.Sprintf("%d/%d", s[0], s[1]))
				} else {
					vals = append(vals, "—")
				}
			}
			if ctrl != nil {
				s := ctrl.ScoreByDiff[diff]
				vals = append(vals, fmt.Sprintf("%d/%d", s[0], s[1]))
			} else {
				vals = append(vals, "—")
			}
			printRow(vals, w)
		}
		// Clean questions
		{
			vals := []string{"  干净题"}
			for _, bn := range backendNames {
				r := backendRuns[bn][model]
				if r != nil {
					vals = append(vals, fmt.Sprintf("%d/%d", r.CleanScore[0], r.CleanScore[1]))
				} else {
					vals = append(vals, "—")
				}
			}
			if ctrl != nil {
				vals = append(vals, fmt.Sprintf("%d/%d", ctrl.CleanScore[0], ctrl.CleanScore[1]))
			} else {
				vals = append(vals, "—")
			}
			printRow(vals, w)
		}

		fmt.Printf("%s\n", thin)

		// Diagnostics
		fmt.Println("Diagnostics:")
		diagRows := []struct {
			label  string
			getter func(r *runResult) float64
		}{
			{"  漏报率 M", func(r *runResult) float64 { return r.MissRate }},
			{"  干扰误判率 D", func(r *runResult) float64 { return r.DistractRate }},
			{"  格式错误率 F", func(r *runResult) float64 { return r.FormatErrRate }},
		}
		for _, dr := range diagRows {
			vals := []string{dr.label}
			for _, bn := range backendNames {
				r := backendRuns[bn][model]
				if r != nil {
					vals = append(vals, fmt.Sprintf("%.1f%%", dr.getter(r)))
				} else {
					vals = append(vals, "—")
				}
			}
			vals = append(vals, fmt.Sprintf("%.1f%%", dr.getter(ctrl)))
			printRow(vals, w)
		}

		// Dropped questions note
		anyDropped := false
		for _, bn := range backendNames {
			r := backendRuns[bn][model]
			if r != nil {
				for _, qr := range r.Results {
					if qr.Dropped {
						anyDropped = true
					}
				}
			}
		}
		if anyDropped {
			fmt.Println("  (⊘ = request failed after 3 retries, excluded from scoring)")
		}

		fmt.Printf("%s\n", thin)

		// 判定
		var judgeStrs []string
		for _, bn := range backendNames {
			r := backendRuns[bn][model]
			if r != nil && ctrl.S > 0 {
				r.R = float64(r.S) / float64(ctrl.S) * 100
				j := judgment(r.R)
				judgeStrs = append(judgeStrs, fmt.Sprintf("%s=%s (R=%.1f%%)", bn, j, r.R))
			}
		}
		if len(judgeStrs) > 0 {
			fmt.Printf("判定: %s\n", strings.Join(judgeStrs, "  "))
		}
		fmt.Printf("%s\n", sep)
	}
}

func printRow(vals []string, colWidth int) {
	for i, v := range vals {
		if i == 0 {
			fmt.Printf("%-22s", v)
		} else {
			fmt.Printf("%-*s", colWidth, v)
		}
	}
	fmt.Println()
}

// ──────────────────────────────────────────────────────────────────
// Main
// ──────────────────────────────────────────────────────────────────

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config.yaml")
	backendsFlag := flag.String("backends", "lianxiang-01,dasheng-lianxiang-sc", "comma-separated backend names to test")
	modelsFlag := flag.String("models", "claude-sonnet-5,claude-opus-5", "comma-separated model names")
	timeoutSec := flag.Int("timeout", 60, "per-request timeout in seconds")
	concurrency := flag.Int("concurrency", 5, "max concurrent requests per channel")
	verbose := flag.Bool("v", false, "verbose: print each question raw response")
	saveFlag := flag.Bool("save", false, "save run results to a daily JSON file in log.dir configured in config")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Printf("ERROR: load config %q: %v\n", *cfgPath, err)
		return
	}

	backends := strings.Split(*backendsFlag, ",")
	models := strings.Split(*modelsFlag, ",")
	for i := range backends {
		backends[i] = strings.TrimSpace(backends[i])
	}
	for i := range models {
		models[i] = strings.TrimSpace(models[i])
	}

	// Build backend callers
	httpClient := &http.Client{Timeout: time.Duration(*timeoutSec) * time.Second}
	backendCallers := map[string]*backendCaller{}
	for _, name := range backends {
		var found *config.BackendAPI
		for i := range cfg.Backends {
			if cfg.Backends[i].Name == name {
				found = &cfg.Backends[i]
				break
			}
		}
		if found == nil {
			fmt.Printf("ERROR: backend %q not found in config\n", name)
			return
		}
		backendCallers[name] = &backendCaller{
			name:   name,
			url:    found.URL,
			apiKey: found.APIKey,
			client: httpClient,
		}
	}

	// Build AWS caller
	var awsC *awsCaller
	if cfg.AWS.Region != "" && cfg.AWS.AccessKeyID != "" {
		bc, err := awsproxy.NewBedrockClient(cfg.AWS.Region, cfg.AWS.AccessKeyID, cfg.AWS.SecretAccessKey, cfg.AWS.Socks5Proxy)
		if err != nil {
			fmt.Printf("ERROR: init AWS Bedrock client: %v\n", err)
			return
		}
		awsC = &awsCaller{client: bc, cfg: &cfg.AWS}
	} else {
		fmt.Println("ERROR: AWS config is missing (region/access_key_id). Cannot use aws control channel.")
		return
	}

	fmt.Printf("ModelProbe Run — %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Printf("Config:   %s\n", *cfgPath)
	fmt.Printf("Models:   %s\n", strings.Join(models, ", "))
	fmt.Printf("Backends: %s\n", strings.Join(backends, ", "))
	fmt.Printf("Control:  aws\n")
	fmt.Printf("Timeout:  %ds/request  Concurrency: %d\n\n", *timeoutSec, *concurrency)

	ctx := context.Background()
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(len(questionBank)*(*timeoutSec)*3)*time.Second)
	defer cancel()

	// controlRuns[model] = runResult for aws
	controlRuns := map[string]*runResult{}
	// backendRuns[backendName][model] = runResult
	backendRuns := map[string]map[string]*runResult{}
	for _, bn := range backends {
		backendRuns[bn] = map[string]*runResult{}
	}

	// Run aws control for each model
	for _, model := range models {
		fmt.Printf("→ Running aws control for %s …\n", model)
		results := runQuestions(requestCtx, awsC, model, *concurrency, *verbose, "aws")
		rr := &runResult{Channel: "aws", Model: model, Results: results, TempOmitted: awsC.noTemp}
		rr.compute()
		controlRuns[model] = rr
		fmt.Printf("  aws/%s: S=%d/%d P=%.1f%%\n", model, rr.S, rr.SMax, rr.P)
	}

	// Run each backend for each model
	for _, bn := range backends {
		for _, model := range models {
			fmt.Printf("→ Running %s / %s …\n", bn, model)
			bc := backendCallers[bn]
			results := runQuestions(requestCtx, bc, model, *concurrency, *verbose, bn)
			rr := &runResult{Channel: bn, Model: model, Results: results, TempOmitted: bc.noTemp}
			rr.compute()
			if bc.noTemp {
				rr.TempOmitted = true
			}
			backendRuns[bn][model] = rr

			ctrl := controlRuns[model]
			rVal := 0.0
			if ctrl != nil && ctrl.S > 0 {
				rVal = float64(rr.S) / float64(ctrl.S) * 100
			}
			fmt.Printf("  %s/%s: S=%d/%d P=%.1f%% R=%.1f%%\n", bn, model, rr.S, rr.SMax, rr.P, rVal)
			if rr.TempOmitted {
				fmt.Printf("  (temperature omitted for %s — backend rejected it)\n", bn)
			}
		}
	}

	// Print results
	printResults(models, backends, controlRuns, backendRuns)

	// Save results to daily JSON file if -save is set.
	// NOTE: printResults() assigns rr.R for all backend runs, so this call
	// must come after printResults() to capture the correct R values.
	if *saveFlag {
		if cfg.Log.Dir == "" {
			fmt.Println("\nWARN: -save specified but log.dir is not configured in config — skipping save.")
		} else {
			rec := probeRecord{
				RunAt:       time.Now().Format("2006-01-02 15:04:05"),
				ConfigPath:  *cfgPath,
				Models:      models,
				Backends:    backends,
				TimeoutSec:  *timeoutSec,
				Concurrency: *concurrency,
				ControlRuns: map[string]runResultJSON{},
				BackendRuns: map[string]map[string]runResultJSON{},
			}
			for model, rr := range controlRuns {
				rec.ControlRuns[model] = rr.toJSON()
			}
			for bn, runs := range backendRuns {
				rec.BackendRuns[bn] = map[string]runResultJSON{}
				for model, rr := range runs {
					if rr == nil {
						continue
					}
					rec.BackendRuns[bn][model] = rr.toJSON()
				}
			}
			if path, err := saveResults(cfg.Log.Dir, rec); err != nil {
				fmt.Printf("\nERROR: save results: %v\n", err)
			} else {
				fmt.Printf("\nResults saved → %s\n", path)
			}
		}
	}

	// Summary: check for errors
	hasErr := false
	for _, bn := range backends {
		for _, model := range models {
			rr := backendRuns[bn][model]
			if rr == nil {
				continue
			}
			for _, qr := range rr.Results {
				if qr.Dropped {
					if !hasErr {
						fmt.Println("\nDropped questions (request failures):")
					}
					hasErr = true
					fmt.Printf("  %s/%s/%s: dropped after 3 retries\n", bn, model, qr.QID)
				}
			}
		}
	}

	// Check if AWS also had drops (sync with backend drops for fair S_max)
	for _, model := range models {
		ctrl := controlRuns[model]
		if ctrl == nil {
			continue
		}
		for _, qr := range ctrl.Results {
			if qr.Dropped {
				fmt.Printf("  aws/%s/%s: dropped after 3 retries\n", model, qr.QID)
			}
		}
	}
}

// ensure errors import is used
var _ = errors.New
