# GSMArena 分布式爬虫

一个生产级的高并发爬虫程序，用于全量抓取 GSMArena 手机数据。

## ✨ 核心特性

- **动态代理池**：从 API 获取代理，支持失败剔除和低水位自动补货
- **持久化去重**：使用 BoltDB 记录已抓取 URL，支持断点续传
- **智能重试**：自动识别 403/429/超时等错误，剔除失败代理并重试
- **高并发**：基于 Go 协程和 Colly 框架实现高效并发抓取
- **结构化输出**：数据保存为 JSONL 格式，便于后续处理

## 🛠️ 技术栈

- **语言**: Go 1.20+
- **爬虫框架**: [gocolly/colly](https://github.com/gocolly/colly) v2
- **数据库**: [bbolt](https://github.com/etcd-io/bbolt) (嵌入式 KV 数据库)
- **并发控制**: `sync.RWMutex`

## 📦 安装依赖

```bash
# 克隆仓库
git clone https://github.com/yangbin1322/go-gsmarena.git
cd go-gsmarena

# 下载依赖
go mod download

# 或直接编译（会自动下载依赖）
go build -o gsmarena-crawler
```

## 🚀 快速开始

### 1. 配置代理 API

修改 `main.go` 中的 `ProxyAPIURL` 常量：

```go
const ProxyAPIURL = "http://your-proxy-api.com/get?count=20"
```

**代理 API 要求**：
- 返回格式：`IP:Port\r\n` 或 `IP:Port\n`（每行一个代理）
- 示例响应：
  ```
  1.2.3.4:8080
  5.6.7.8:3128
  9.10.11.12:80
  ```

### 2. 运行爬虫

```bash
# 直接运行
go run .

# 或编译后运行
go build -o gsmarena-crawler
./gsmarena-crawler
```

### 3. 查看结果

- **数据输出**: `results.jsonl` (每行一个 JSON 对象)
- **去重数据库**: `crawler.db` (BoltDB 文件，记录已访问 URL)

## 📊 数据格式

每条记录为一个 JSON 对象（JSONL 格式）：

```json
{
  "model_name": "Apple iPhone 15 Pro Max",
  "brand": "Apple",
  "release_date": "2023, September 22",
  "url": "https://www.gsmarena.com/apple_iphone_15_pro_max-12548.php",
  "specs": {
    "Network": "GSM / CDMA / HSPA / EVDO / LTE / 5G",
    "Display": "6.7 inches, 1290 x 2796 pixels",
    "Chipset": "Apple A17 Pro (3 nm)",
    "Memory": "256GB 8GB RAM, 512GB 8GB RAM, 1TB 8GB RAM",
    ...
  },
  "crawled_at": "2025-12-02T10:30:45Z"
}
```

## 🔧 配置参数

在 `main.go` 中可调整以下参数：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `ProxyAPIURL` | - | 代理 API 地址 |
| `MinProxyThreshold` | 5 | 代理池最低存活数量 |
| `Parallelism` | 10 | 并发请求数 |
| `RequestTimeout` | 15s | 请求超时时间 |
| `MinDelay` / `MaxDelay` | 500ms / 1000ms | 随机延迟范围 |

## 📁 项目结构

```
go-gsmarena/
├── main.go           # 主程序：爬虫核心逻辑
├── proxy_pool.go     # 代理池管理模块
├── storage.go        # BoltDB 持久化去重模块
├── go.mod            # Go 模块依赖
├── README.md         # 项目说明文档
├── crawler.db        # BoltDB 数据库（运行时生成）
└── results.jsonl     # 输出数据（运行时生成）
```

## 🎯 工作流程

1. **初始化**：
   - 打开 BoltDB 数据库（去重用）
   - 从 API 获取初始代理列表
   - 打开输出文件 `results.jsonl`

2. **抓取流程**：
   ```
   品牌列表页 (makers.php3)
       ↓
   品牌手机列表页 (apple-phones-48.php)
       ↓
   手机详情页 (apple_iphone_15_pro_max-12548.php)
       ↓
   解析数据 + 去重检查 + 保存到 JSONL
   ```

3. **错误处理**：
   - 遇到 403/429/503：剔除当前代理，重试请求
   - 遇到超时/连接失败：剔除代理，重试请求
   - 遇到 404：跳过该页面，不重试

4. **自动补货**：
   - 代理池为空：强制同步刷新
   - 代理数 < 阈值：异步触发补货

## 🛡️ 反爬策略

- ✅ 动态代理轮换
- ✅ 随机 User-Agent
- ✅ 随机延迟 (500ms-1s)
- ✅ 并发控制 (10 并发)
- ✅ 短超时快速切换
- ✅ 故障代理自动剔除

## ⚠️ 注意事项

1. **代理质量**：请使用高质量的代理服务，低质量代理会导致频繁失败
2. **合法性**：请遵守目标网站的 `robots.txt` 和服务条款
3. **速率限制**：建议根据代理质量调整并发数和延迟时间
4. **数据备份**：定期备份 `crawler.db` 和 `results.jsonl`

## 📝 日志说明

程序会输出详细的运行日志：

```
[请求] https://www.gsmarena.com/apple-phones-48.php (代理: http://1.2.3.4:8080)
[响应] https://www.gsmarena.com/apple-phones-48.php (状态码: 200, 大小: 45231 bytes)
[发现] 手机详情页: https://www.gsmarena.com/apple_iphone_15_pro_max-12548.php
[成功] 已抓取并标记: https://www.gsmarena.com/apple_iphone_15_pro_max-12548.php
[保存] Apple iPhone 15 Pro Max (Apple)
```

## 🐛 常见问题

**Q: 代理池为空怎么办？**
A: 检查 `ProxyAPIURL` 是否正确，确保 API 返回格式为 `IP:Port\n`。

**Q: 为什么一直报 403 错误？**
A: 可能是代理质量差或被封禁，建议更换代理服务商。

**Q: 如何继续上次未完成的任务？**
A: 程序会自动读取 `crawler.db`，跳过已抓取的 URL，直接运行即可。

**Q: 如何清空数据重新抓取？**
A: 删除 `crawler.db` 和 `results.jsonl` 文件，然后重新运行。

## 📄 许可证

MIT License

## 👨‍💻 作者

yangbin1322

---

**免责声明**：本项目仅供学习交流使用，请勿用于商业用途。使用本工具产生的任何法律责任由使用者自行承担。
