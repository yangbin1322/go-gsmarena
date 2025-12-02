# 开发文档：GSMArena 分布式爬虫 (Go/Colly + 动态代理 + 持久化去重) 



## 1. 项目概述

目标：开发一个生产级的高并发爬虫，全量抓取 GSMArena 手机数据。

核心痛点解决：

1. **反爬风控**：通过动态代理池（API获取、自动轮换、失败剔除）解决 IP 封锁。
2. **断点续传/去重**：使用嵌入式数据库记录已抓取 URL，防止重启后重复工作。
3. **高并发**：利用 Go 协程实现快速采集。

## 2. 技术栈

- **语言**: Go (Golang) >= 1.20
- **爬虫框架**: `github.com/gocolly/colly/v2`
- **持久化存储 (去重)**: `go.etcd.io/bbolt` (嵌入式 KV 数据库，轻量且并发安全)
- **辅助库**: `sync` (并发锁), `encoding/json`

## 3. 模块详细设计

### 3.1 动态代理管理模块 (`proxy_pool.go`)

**功能**：维护一个健康的代理 IP 池，支持“API 补货”和“脏代理剔除”。

**核心逻辑**：

1. **结构体设计**:

   Go

   ```
   type ProxyManager struct {
       apiUrl       string       // 代理 API 地址
       minThreshold int          // 最低存活阈值 (如 5 个)
       proxies      []string     // 代理切片 (如 "http://1.1.1.1:80")
       lock         sync.RWMutex // 读写锁
   }
   ```

2. **API 解析**:

   - 请求 API，获取响应文本。
   - 按 `\r\n` 或 `\n` 分割字符串。
   - **格式化**: 必须检测并添加协议头（如无 `http://` 则自动补全），确保 Colly 可识别。

3. **获取代理 (GetProxy)**:

   - 实现 `colly.ProxyFunc`。
   - 使用 Round-Robin (轮询) 算法返回代理。
   - **自动补货**: 每次获取时，若 `len(proxies) == 0`，强制同步刷新；若 `len(proxies) < minThreshold`，异步触发刷新。

4. **移除代理 (RemoveProxy)**:

   - 当请求失败时调用。
   - 加写锁，从切片中移除该 IP。
   - 移除后立即检查水位，若过低则触发 API 刷新。

### 3.2 持久化去重模块 (`storage.go`)

**功能**：使用 BoltDB 记录已成功抓取的详情页 URL (BloomFilter 存在误判且无持久化，故选用 KV DB)。

**接口定义**:

Go

```
type Storage interface {
    // 检查 URL 是否已存在
    IsVisited(url string) bool
    // 标记 URL 为已访问
    MarkVisited(url string) error
    // 关闭数据库连接
    Close() error
}
```

**BoltDB 实现**:

- **Bucket 名称**: `"visited_urls"`
- **Key**: URL 字符串 (或 URL 的 MD5 哈希，建议直接存 URL 以便调试)。
- **Value**: 空字节 `[]byte{}` 或时间戳 (节省空间)。
- **逻辑**: 程序启动时打开 `crawler.db` 文件，结束时关闭。

### 3.3 爬虫核心配置 (`crawler.go` / `main.go`)

**Colly 初始化**:

- `AllowedDomains`: `www.gsmarena.com`, `gsmarena.com`
- `Async`: `true` (异步并发)
- **超时设置**: `DialContext` 设置为 **10-15秒** (短效代理不稳定，需快速超时)。

**反爬策略**:

- **User-Agent**: 设置随机 UA 或固定为最新 Chrome。
- **LimitRule**:
  - `Parallelism`: **10** (根据代理质量调整)。
  - `RandomDelay`: **500ms - 1s** (配合动态代理使用)。

### 3.4 页面解析与数据流

1. **入口**: `https://www.gsmarena.com/makers.php3`
2. **品牌页解析**: 提取所有 `makers` 链接 -> `Visit()`
3. **列表页解析**:
   - 提取手机详情页链接。
   - **去重检查**: 在 `Visit()` 详情页之前，调用 `Storage.IsVisited(url)`。如果为 `true`，跳过；否则 `Visit()`。
   - **翻页**: 识别 "Next" 按钮，递归访问。
4. **详情页解析**:
   - 提取字段: `ModelName`, `ReleaseDate`, `Specs` (KV Map)。
   - **成功回调**: 在 `OnScraped` 或数据提取成功后，调用 `Storage.MarkVisited(url)` 写入数据库。
   - **数据保存**: 将结果追加写入 `results.jsonl` 文件。

### 3.5 错误处理与重试 (`OnError`)

**必须实现的风控处理逻辑**:

- 获取当前请求使用的 `ProxyURL`。
- 判断错误类型:
  - 如果是 **404**: 忽略，不重试，但可标记为 Visited 防止死循环。
  - 如果是 **403, 429, 503, Timeout, Connection Refused**:
    1. **Log**: 记录错误信息。
    2. **Ban**: 调用 `ProxyManager.RemoveProxy(proxyUrl)` 剔除坏代理。
    3. **Retry**: 调用 `r.Request.Retry()` 重新入队 (Colly 会自动换新代理)。

## 4. 交付代码结构规范

请 AI Agent 严格按照以下文件结构生成代码：

1. **`go.mod`**: 依赖定义。
2. **`storage.go`**: 封装 BoltDB 的 `IsVisited` 和 `MarkVisited` 逻辑。
3. **`proxy_pool.go`**: 复杂的动态代理池实现（含锁、API 请求、自动补货）。
4. **`main.go`**:
   - 初始化 Storage 和 ProxyManager。
   - 配置 Colly (OnRequest, OnError, OnHTML)。
   - 定义数据结构 `Phone`。
   - 主流程控制。