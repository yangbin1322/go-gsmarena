package main

import (
	"fmt"
	"log"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Storage 定义持久化存储接口，用于 URL 去重
type Storage interface {
	// IsVisited 检查 URL 是否已访问过
	IsVisited(url string) bool
	// MarkVisited 标记 URL 为已访问
	MarkVisited(url string) error
	// Close 关闭数据库连接
	Close() error
}

// BoltStorage 基于 BoltDB 的持久化存储实现
type BoltStorage struct {
	db         *bolt.DB
	bucketName []byte
}

// NewBoltStorage 创建新的 BoltDB 存储实例
// dbPath: 数据库文件路径 (如 "crawler.db")
// bucketName: Bucket 名称 (如 "visited_urls")
func NewBoltStorage(dbPath, bucketName string) (*BoltStorage, error) {
	// 打开 BoltDB 数据库，设置超时时间防止锁死
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{
		Timeout: 3 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("无法打开数据库: %w", err)
	}

	storage := &BoltStorage{
		db:         db,
		bucketName: []byte(bucketName),
	}

	// 初始化 Bucket (如果不存在则创建)
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(storage.bucketName)
		if err != nil {
			return fmt.Errorf("创建 Bucket 失败: %w", err)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}

	log.Printf("BoltDB 存储初始化成功: %s (Bucket: %s)", dbPath, bucketName)
	return storage, nil
}

// IsVisited 检查 URL 是否已被访问过
// 返回 true 表示已访问，false 表示未访问
func (s *BoltStorage) IsVisited(url string) bool {
	var exists bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucketName)
		if b == nil {
			return fmt.Errorf("Bucket 不存在")
		}
		// 通过 Get 方法检查 Key 是否存在
		val := b.Get([]byte(url))
		exists = val != nil
		return nil
	})

	if err != nil {
		log.Printf("检查 URL 是否访问时出错: %v", err)
		return false
	}

	return exists
}

// MarkVisited 将 URL 标记为已访问
// 存储格式: Key=URL, Value=时间戳（用于调试和统计）
func (s *BoltStorage) MarkVisited(url string) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucketName)
		if b == nil {
			return fmt.Errorf("Bucket 不存在")
		}

		// 存储时间戳作为 Value（也可以存储空字节以节省空间）
		timestamp := []byte(time.Now().Format(time.RFC3339))
		return b.Put([]byte(url), timestamp)
	})

	if err != nil {
		return fmt.Errorf("标记 URL 为已访问失败: %w", err)
	}

	return nil
}

// Close 关闭数据库连接
func (s *BoltStorage) Close() error {
	if s.db != nil {
		log.Println("关闭 BoltDB 连接...")
		return s.db.Close()
	}
	return nil
}

// GetStats 获取数据库统计信息（可选功能，用于调试）
func (s *BoltStorage) GetStats() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(s.bucketName)
		if b == nil {
			return fmt.Errorf("Bucket 不存在")
		}
		// 统计 Bucket 中的键值对数量
		stats := b.Stats()
		count = stats.KeyN
		return nil
	})
	return count, err
}
