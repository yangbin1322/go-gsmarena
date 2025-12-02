package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyManager 动态代理池管理器
// 负责从 API 获取代理、维护健康代理列表、实现故障剔除和自动补货
type ProxyManager struct {
	apiURL       string       // 代理 API 地址
	minThreshold int          // 最低存活代理数量阈值
	proxies      []string     // 代理列表 (格式: "http://IP:Port")
	lock         sync.RWMutex // 读写锁，保证并发安全
	currentIndex int          // Round-Robin 轮询索引
	isRefreshing bool         // 是否正在刷新代理（防止并发刷新）
	refreshLock  sync.Mutex   // 刷新操作的互斥锁
}

// NewProxyManager 创建新的代理管理器实例
// apiURL: 代理 API 地址，返回格式为 "IP:Port\r\n" 或 "IP:Port\n"
// minThreshold: 最低存活代理数量，低于此值将触发自动补货
func NewProxyManager(apiURL string, minThreshold int) *ProxyManager {
	pm := &ProxyManager{
		apiURL:       apiURL,
		minThreshold: minThreshold,
		proxies:      make([]string, 0),
		currentIndex: 0,
		isRefreshing: false,
	}

	// 初始化时同步加载代理
	log.Println("初始化代理池...")
	if err := pm.fetchProxies(); err != nil {
		log.Printf("警告: 初始化代理池失败: %v", err)
	} else {
		log.Printf("代理池初始化成功，当前代理数量: %d", pm.Count())
	}

	return pm
}

// fetchProxies 从 API 获取代理并更新代理池
// 此方法会阻塞，直到成功获取代理或失败
func (pm *ProxyManager) fetchProxies() error {
	log.Printf("正在从 API 获取代理: %s", pm.apiURL)

	// 创建 HTTP 客户端，设置超时
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// 发送 GET 请求
	resp, err := client.Get(pm.apiURL)
	if err != nil {
		return fmt.Errorf("请求代理 API 失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("代理 API 返回非 200 状态码: %d", resp.StatusCode)
	}

	// 读取响应内容
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取代理 API 响应失败: %w", err)
	}

	// 解析代理列表
	rawProxies := strings.Split(string(body), "\n")
	newProxies := make([]string, 0)
	fmt.Println("代理列表", string(body))
	for _, line := range rawProxies {
		// 清理空格和回车符
		line = strings.TrimSpace(line)
		line = strings.ReplaceAll(line, "\r", "")

		// 跳过空行
		if line == "" {
			continue
		}

		// 格式化代理地址：确保有协议头
		proxy := pm.formatProxy(line)
		if proxy != "" {
			newProxies = append(newProxies, proxy)
		}
	}

	// 更新代理池（加写锁）
	pm.lock.Lock()
	pm.proxies = newProxies
	pm.currentIndex = 0 // 重置索引
	pm.lock.Unlock()

	log.Printf("代理池更新成功，新增 %d 个代理", len(newProxies))
	return nil
}

// formatProxy 格式化代理地址，确保包含协议头
// 输入: "1.2.3.4:8080" 或 "http://1.2.3.4:8080"
// 输出: "http://1.2.3.4:8080"
func (pm *ProxyManager) formatProxy(raw string) string {
	raw = strings.TrimSpace(raw)

	// 如果已经包含协议头，直接返回
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}

	// 添加 http:// 协议头
	return "http://" + raw
}

// GetProxy 获取一个可用代理（实现 colly.ProxyFunc 接口）
// 使用 Round-Robin 算法轮询返回代理
// 自动触发低水位补货机制
func (pm *ProxyManager) GetProxy(r *http.Request) (*url.URL, error) {
	pm.lock.RLock()
	proxyCount := len(pm.proxies)
	pm.lock.RUnlock()

	// 情况 1: 代理池为空，强制同步刷新
	if proxyCount == 0 {
		log.Println("代理池为空，强制同步刷新...")
		if err := pm.fetchProxies(); err != nil {
			return nil, fmt.Errorf("无可用代理且刷新失败: %w", err)
		}
		// 刷新后重新获取计数
		pm.lock.RLock()
		proxyCount = len(pm.proxies)
		pm.lock.RUnlock()

		if proxyCount == 0 {
			return nil, fmt.Errorf("刷新后仍无可用代理")
		}
	}

	// 情况 2: 代理数量低于阈值，异步触发刷新
	if proxyCount < pm.minThreshold {
		go pm.asyncRefresh()
	}

	// 使用 Round-Robin 算法选择代理
	pm.lock.Lock()
	if len(pm.proxies) == 0 {
		pm.lock.Unlock()
		return nil, fmt.Errorf("无可用代理")
	}

	// 获取当前代理
	proxyStr := pm.proxies[pm.currentIndex]
	// 更新索引（循环）
	pm.currentIndex = (pm.currentIndex + 1) % len(pm.proxies)
	pm.lock.Unlock()

	// 解析代理 URL
	proxyURL, err := url.Parse(proxyStr)
	if err != nil {
		return nil, fmt.Errorf("解析代理 URL 失败: %w", err)
	}

	return proxyURL, nil
}

// asyncRefresh 异步刷新代理池（防止重复刷新）
func (pm *ProxyManager) asyncRefresh() {
	pm.refreshLock.Lock()
	defer pm.refreshLock.Unlock()

	// 如果已经在刷新中，直接返回
	if pm.isRefreshing {
		return
	}

	pm.isRefreshing = true
	defer func() {
		pm.isRefreshing = false
	}()

	log.Println("代理数量低于阈值，触发异步补货...")
	if err := pm.fetchProxies(); err != nil {
		log.Printf("异步刷新代理失败: %v", err)
	}
}

// RemoveProxy 从代理池中移除失败的代理
// proxyURL: 需要移除的代理地址（完整 URL 格式）
func (pm *ProxyManager) RemoveProxy(proxyURL string) {
	pm.lock.Lock()
	defer pm.lock.Unlock()

	// 遍历查找并移除
	for i, proxy := range pm.proxies {
		if proxy == proxyURL {
			// 使用切片操作移除元素
			pm.proxies = append(pm.proxies[:i], pm.proxies[i+1:]...)
			log.Printf("已移除失败代理: %s，剩余代理数量: %d", proxyURL, len(pm.proxies))

			// 调整 currentIndex（防止越界）
			if pm.currentIndex >= len(pm.proxies) && len(pm.proxies) > 0 {
				pm.currentIndex = 0
			}

			// 移除后检查是否低于阈值，触发补货
			if len(pm.proxies) < pm.minThreshold {
				go pm.asyncRefresh()
			}

			return
		}
	}

	log.Printf("警告: 尝试移除的代理不在池中: %s", proxyURL)
}

// Count 返回当前代理池中的代理数量（线程安全）
func (pm *ProxyManager) Count() int {
	pm.lock.RLock()
	defer pm.lock.RUnlock()
	return len(pm.proxies)
}

// GetAll 返回所有代理列表的副本（用于调试）
func (pm *ProxyManager) GetAll() []string {
	pm.lock.RLock()
	defer pm.lock.RUnlock()

	// 返回副本以防止外部修改
	proxiesCopy := make([]string, len(pm.proxies))
	copy(proxiesCopy, pm.proxies)
	return proxiesCopy
}
