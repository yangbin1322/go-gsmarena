package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gocolly/colly/v2"
)

// Phone 手机数据结构
type Phone struct {
	ModelName   string            `json:"model_name"`   // 手机型号名称
	Brand       string            `json:"brand"`        // 品牌
	ReleaseDate string            `json:"release_date"` // 发布日期
	URL         string            `json:"url"`          // 详情页 URL
	Specs       map[string]string `json:"specs"`        // 规格参数（键值对）
	CrawledAt   string            `json:"crawled_at"`   // 抓取时间
}

// 全局配置常量
const (
	// 代理 API 地址（请替换为实际的代理 API）
	ProxyAPIURL = "http://your-proxy-api.com/get?count=20"

	// 代理池最低阈值
	MinProxyThreshold = 5

	// BoltDB 数据库文件路径
	DBPath = "crawler.db"

	// BoltDB Bucket 名称
	BucketName = "visited_urls"

	// 输出文件路径
	OutputFile = "results.jsonl"

	// Colly 并发数
	Parallelism = 10

	// 随机延迟范围（毫秒）
	MinDelay = 500
	MaxDelay = 1000

	// 请求超时时间（秒）
	RequestTimeout = 15
)

// 全局变量
var (
	storage      Storage       // 持久化存储
	proxyManager *ProxyManager // 代理管理器
	outputFile   *os.File      // 输出文件句柄
	outputMutex  sync.Mutex    // 输出文件写入锁
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("========== GSMArena 爬虫启动 ==========")

	// 1. 初始化持久化存储
	var err error
	storage, err = NewBoltStorage(DBPath, BucketName)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}
	defer storage.Close()

	// 2. 初始化代理管理器
	proxyManager = NewProxyManager(ProxyAPIURL, MinProxyThreshold)
	if proxyManager.Count() == 0 {
		log.Println("警告: 代理池为空，爬虫可能会因 IP 限制而失败")
	}

	// 3. 打开输出文件
	outputFile, err = os.OpenFile(OutputFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("打开输出文件失败: %v", err)
	}
	defer outputFile.Close()

	// 4. 创建 Colly 爬虫实例
	c := createCollector()

	// 5. 设置回调函数
	setupCallbacks(c)

	// 6. 启动爬虫
	log.Println("开始抓取 GSMArena 数据...")
	startURL := "https://www.gsmarena.com/makers.php3"
	if err := c.Visit(startURL); err != nil {
		log.Printf("访问起始页面失败: %v", err)
	}

	// 7. 等待所有异步请求完成
	c.Wait()

	// 8. 输出统计信息
	printStats()

	log.Println("========== 爬虫任务完成 ==========")
}

// createCollector 创建并配置 Colly 爬虫实例
func createCollector() *colly.Collector {
	c := colly.NewCollector(
		// 限制爬取域名
		colly.AllowedDomains("www.gsmarena.com", "gsmarena.com"),
		// 启用异步模式
		colly.Async(true),
	)

	// 配置 HTTP 传输层（设置超时和代理）
	c.WithTransport(&http.Transport{
		// 设置连接超时
		DialContext: (&net.Dialer{
			Timeout:   RequestTimeout * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// 最大空闲连接
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		// 响应头超时
		ResponseHeaderTimeout: RequestTimeout * time.Second,
		// TLS 握手超时
		TLSHandshakeTimeout: 10 * time.Second,
	})

	// 设置代理
	c.SetProxyFunc(proxyManager.GetProxy)

	// 设置限速规则
	err := c.Limit(&colly.LimitRule{
		DomainGlob:  "*gsmarena.com*",
		Parallelism: Parallelism,
		RandomDelay: time.Duration(MinDelay) * time.Millisecond,
		Delay:       time.Duration(MaxDelay) * time.Millisecond,
	})
	if err != nil {
		log.Fatalf("设置限速规则失败: %v", err)
	}

	// 设置 User-Agent
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		r.Headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
		r.Headers.Set("Accept-Language", "en-US,en;q=0.9")
	})

	return c
}

// setupCallbacks 设置 Colly 回调函数
func setupCallbacks(c *colly.Collector) {
	// OnRequest: 请求发送前
	c.OnRequest(func(r *colly.Request) {
		proxyURL := "直连"
		if r.ProxyURL != "" {
			proxyURL = r.ProxyURL
		}
		log.Printf("[请求] %s (代理: %s)", r.URL, proxyURL)
	})

	// OnError: 请求失败处理（核心：代理剔除 + 重试）
	c.OnError(func(r *colly.Response, err error) {
		statusCode := r.StatusCode
		requestURL := r.Request.URL.String()
		proxyURL := r.Request.ProxyURL

		log.Printf("[错误] URL=%s, StatusCode=%d, Error=%v, Proxy=%s",
			requestURL, statusCode, err, proxyURL)

		// 判断是否需要剔除代理并重试
		shouldRetry := false

		switch {
		case statusCode == 404:
			// 404 不重试，但标记为已访问防止死循环
			log.Printf("[404] 页面不存在，跳过: %s", requestURL)
			_ = storage.MarkVisited(requestURL)

		case statusCode == 403 || statusCode == 429 || statusCode == 503:
			// 403/429/503: 代理被封禁或限流
			log.Printf("[风控] 状态码 %d，剔除代理并重试", statusCode)
			shouldRetry = true

		case err != nil && (strings.Contains(err.Error(), "timeout") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "EOF")):
			// 超时或连接失败
			log.Printf("[超时/连接失败] 剔除代理并重试")
			shouldRetry = true

		default:
			// 其他错误，记录但不重试
			log.Printf("[其他错误] 不重试: %v", err)
		}

		// 执行代理剔除和重试
		if shouldRetry {
			if proxyURL != "" {
				proxyManager.RemoveProxy(proxyURL)
			}
			// 重新入队请求（Colly 会自动使用新代理）
			if err := r.Request.Retry(); err != nil {
				log.Printf("[重试失败] %s: %v", requestURL, err)
			} else {
				log.Printf("[已重试] %s", requestURL)
			}
		}
	})

	// OnHTML: 品牌列表页 (makers.php3)
	c.OnHTML(".st-text", func(e *colly.HTMLElement) {
		brandName := e.Text
		brandURL := e.Request.AbsoluteURL(e.Attr("href"))

		if brandURL != "" && strings.Contains(brandURL, ".php") {
			log.Printf("[品牌] %s -> %s", brandName, brandURL)
			e.Request.Visit(brandURL)
		}
	})

	// OnHTML: 品牌手机列表页
	c.OnHTML(".makers", func(e *colly.HTMLElement) {
		// 提取手机详情页链接
		e.ForEach("li a", func(_ int, el *colly.HTMLElement) {
			phoneURL := el.Request.AbsoluteURL(el.Attr("href"))

			// 去重检查：如果已访问过，跳过
			if storage.IsVisited(phoneURL) {
				log.Printf("[跳过] 已访问: %s", phoneURL)
				return
			}

			// 访问详情页
			log.Printf("[发现] 手机详情页: %s", phoneURL)
			el.Request.Visit(phoneURL)
		})

		// 翻页：查找 "Next" 按钮
		e.ForEach(".nav-pages a", func(_ int, el *colly.HTMLElement) {
			if strings.Contains(strings.ToLower(el.Text), "next") {
				nextPageURL := el.Request.AbsoluteURL(el.Attr("href"))
				log.Printf("[翻页] %s", nextPageURL)
				el.Request.Visit(nextPageURL)
			}
		})
	})

	// OnHTML: 手机详情页解析
	c.OnHTML(".specs-phone-name-title, #specs-list", func(e *colly.HTMLElement) {
		if e.Request.URL.Path == "/makers.php3" {
			return // 跳过品牌列表页
		}

		// 检查是否为详情页（包含规格表）
		if e.Attr("id") != "specs-list" {
			return
		}

		phoneURL := e.Request.URL.String()

		// 再次检查去重（防止并发重复抓取）
		if storage.IsVisited(phoneURL) {
			return
		}

		// 提取手机名称
		modelName := e.DOM.ParentsUntil("body").Find(".specs-phone-name-title").Text()
		modelName = strings.TrimSpace(modelName)

		// 提取品牌（从 URL 推断）
		brand := extractBrandFromURL(phoneURL)

		// 提取规格参数
		specs := make(map[string]string)
		e.ForEach("table tr", func(_ int, row *colly.HTMLElement) {
			key := strings.TrimSpace(row.ChildText(".ttl"))
			value := strings.TrimSpace(row.ChildText(".nfo"))
			if key != "" {
				specs[key] = value
			}
		})

		// 提取发布日期
		releaseDate := specs["Released"]
		if releaseDate == "" {
			releaseDate = "Unknown"
		}

		// 构建 Phone 对象
		phone := Phone{
			ModelName:   modelName,
			Brand:       brand,
			ReleaseDate: releaseDate,
			URL:         phoneURL,
			Specs:       specs,
			CrawledAt:   time.Now().Format(time.RFC3339),
		}

		// 保存数据
		savePhone(phone)

		// 标记为已访问
		if err := storage.MarkVisited(phoneURL); err != nil {
			log.Printf("[错误] 标记 URL 失败: %v", err)
		} else {
			log.Printf("[成功] 已抓取并标记: %s", phoneURL)
		}
	})

	// OnResponse: 响应成功
	c.OnResponse(func(r *colly.Response) {
		log.Printf("[响应] %s (状态码: %d, 大小: %d bytes)",
			r.Request.URL, r.StatusCode, len(r.Body))
	})

	// OnScraped: 页面抓取完成
	c.OnScraped(func(r *colly.Response) {
		log.Printf("[完成] %s", r.Request.URL)
	})
}

// extractBrandFromURL 从 URL 中提取品牌名称
// 例如: https://www.gsmarena.com/apple-phones-48.php -> "Apple"
func extractBrandFromURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		lastPart := parts[len(parts)-1]
		// 移除 "-phones-xx.php" 后缀
		brandPart := strings.Split(lastPart, "-phones-")
		if len(brandPart) > 0 {
			brand := strings.ReplaceAll(brandPart[0], "-", " ")
			return strings.Title(strings.ToLower(brand))
		}
	}
	return "Unknown"
}

// savePhone 将手机数据保存为 JSONL 格式
func savePhone(phone Phone) {
	outputMutex.Lock()
	defer outputMutex.Unlock()

	// 序列化为 JSON
	data, err := json.Marshal(phone)
	if err != nil {
		log.Printf("[错误] JSON 序列化失败: %v", err)
		return
	}

	// 写入文件（每行一个 JSON 对象）
	if _, err := outputFile.Write(append(data, '\n')); err != nil {
		log.Printf("[错误] 写入文件失败: %v", err)
		return
	}

	log.Printf("[保存] %s (%s)", phone.ModelName, phone.Brand)
}

// printStats 输出统计信息
func printStats() {
	// 获取已访问 URL 数量
	if boltStorage, ok := storage.(*BoltStorage); ok {
		count, err := boltStorage.GetStats()
		if err != nil {
			log.Printf("获取统计信息失败: %v", err)
		} else {
			log.Printf("========== 统计信息 ==========")
			log.Printf("已抓取 URL 数量: %d", count)
			log.Printf("剩余代理数量: %d", proxyManager.Count())
			log.Printf("输出文件: %s", OutputFile)
			log.Printf("==============================")
		}
	}
}

// init 初始化函数：设置信号处理（优雅退出）
func init() {
	// 捕获 Ctrl+C 信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\n收到中断信号，正在优雅退出...")
		if storage != nil {
			storage.Close()
		}
		if outputFile != nil {
			outputFile.Close()
		}
		os.Exit(0)
	}()
}
