package common

import (
	"bytes"
	"container/list"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/puzpuzpuz/xsync"
	log "github.com/sirupsen/logrus"
)

// Configuration struct for FileCache
type FileCacheConfig struct {
	EvictionTick time.Duration
	DirPath      string
	DiskBytesMax int64
	RAMBytesMax  int64
}

var (
	cacheReadsRAM = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filecache_reads_ram_total",
		Help: "Total number of cache read operations from RAM.",
	})

	cacheReadsMissed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filecache_reads_missed_total",
		Help: "Total number of cache read operations that failed.",
	})

	cacheReadsDisk = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filecache_reads_disk_total",
		Help: "Total number of cache read operations from disk.",
	})

	cacheWrites = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filecache_writes_total",
		Help: "Total number of cache write operations.",
	})

	cacheFiles = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "filecache_files",
		Help: "Current number of files in the cache.",
	})

	cacheSizeRAM = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "filecache_size_ram_bytes",
		Help: "Current size of the cache in RAM (bytes).",
	})

	cacheSizeDisk = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "filecache_size_disk_bytes",
		Help: "Current size of the cache on disk (bytes).",
	})

	evictionRAMCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filecache_evictions_ram_total",
		Help: "Total number of evictions from RAM.",
	})

	evictionDiskCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "filecache_evictions_disk_total",
		Help: "Total number of evictions from disk.",
	})
)

func init() {
	prometheus.MustRegister(
		cacheFiles,
		cacheReadsDisk,
		cacheReadsMissed,
		cacheReadsRAM,
		cacheSizeDisk,
		cacheSizeRAM,
		cacheWrites,
		evictionDiskCounter,
		evictionRAMCounter)
}

type fileCacheEntry struct {
	Data     []byte
	InMemory bool
	Mutex    sync.Mutex
	Size     int64
}

type FileCache struct {
	index           *xsync.MapOf[string, *fileCacheEntry]
	config          FileCacheConfig
	evictionTicker  *time.Ticker
	mruList         *list.List
	mruMap          map[string]*list.Element
	mutex           sync.Mutex
	usedDiskBytes   int64
	usedMemoryBytes int64
}

func NewFileCache(config FileCacheConfig) *FileCache {
	fc := &FileCache{
		index:          xsync.NewMapOf[*fileCacheEntry](),
		config:         config,
		evictionTicker: time.NewTicker(config.EvictionTick),
		mruList:        list.New(),
		mruMap:         make(map[string]*list.Element),
	}

	return fc
}

func (fc *FileCache) init() error {
	fc.mutex.Lock()
	defer fc.mutex.Unlock()

	// make disk folder
	if _, err := os.Stat(fc.config.DirPath); os.IsNotExist(err) {
		if err := os.MkdirAll(fc.config.DirPath, 0755); err != nil {
			return err
		}
	}

	// scan files
	fsys := os.DirFS(fc.config.DirPath)
	pattern := filepath.Join(fc.config.DirPath, "**/*")
	files, err := doublestar.Glob(fsys, pattern)
	if err != nil {
		return err
	}

	// index files
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return err
		}

		if !info.IsDir() {
			fileName := filepath.Base(file)
			entry := &fileCacheEntry{
				Data:     nil,
				Size:     info.Size(),
				InMemory: false,
			}

			fc.index.Store(fileName, entry)
			fc.usedDiskBytes += entry.Size
		}
	}

	return nil
}

func (fc *FileCache) Read(filePath string) (io.Reader, error) {
	entry, ok := fc.index.Load(filePath)
	if !ok {
		// it's not in the index, so we don't have it.
		return nil, os.ErrNotExist
	}

	// lock this while we do the read...
	entry.Mutex.Lock()
	defer entry.Mutex.Unlock()

	// because if its not in memory, we must modify it...
	if !entry.InMemory {
		cacheReadsDisk.Inc()

		data, err := os.ReadFile(filepath.Join(fc.config.DirPath, filePath))
		if err != nil {
			return nil, err
		}

		entry.Data = data
		entry.InMemory = true

		fc.mutex.Lock()
		fc.usedMemoryBytes += int64(len(data))
		fc.updateMRU(filePath)
		fc.updateCacheMetrics()
		fc.mutex.Unlock()
	} else {
		cacheReadsRAM.Inc()

		fc.mutex.Lock()
		fc.updateMRU(filePath)
		fc.mutex.Unlock()
	}
	return bytes.NewReader(entry.Data), nil
}

func (fc *FileCache) Write(fileName string, in io.Reader) error {
	cacheWrites.Inc()

	// get or create the index entry atomically
	fc.mutex.Lock()
	var entrySizeStart int64
	entry, ok := fc.index.Load(fileName)
	if !ok {
		entry = &fileCacheEntry{}
		fc.index.Store(fileName, entry)
	} else {
		entrySizeStart = entry.Size
	}

	// lock the entry and then release the cache
	entry.Mutex.Lock()
	defer entry.Mutex.Unlock()
	fc.mutex.Unlock()

	// read all the data
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}

	// write out the file
	fullPath := filepath.Join(fc.config.DirPath, fileName)
	if err := os.WriteFile(fullPath, data, 0664); err != nil {
		return err
	}

	// update the entry (this is safe cause we have the entry locked)
	entry.Data = data
	entry.Size = int64(len(data))
	entry.InMemory = true

	// lock the cache before updating stats
	fc.mutex.Lock()
	fc.usedMemoryBytes += entry.Size
	fc.usedMemoryBytes -= entrySizeStart
	fc.updateMRU(fileName)
	fc.updateCacheMetrics()
	fc.mutex.Unlock()

	return nil
}

func (fc *FileCache) updateMRU(fileName string) {
	if elem, exists := fc.mruMap[fileName]; exists {
		fc.mruList.MoveToFront(elem)
		return
	}
	elem := fc.mruList.PushFront(fileName)
	fc.mruMap[fileName] = elem
}

func (fc *FileCache) updateCacheMetrics() {
	cacheFiles.Set(float64(fc.mruList.Len()))
	cacheSizeRAM.Set(float64(fc.usedMemoryBytes))
	cacheSizeDisk.Set(float64(fc.usedDiskBytes))
}

func (fc *FileCache) Start() error {
	if err := fc.init(); err != nil {
		return fmt.Errorf("failed to initialize cache folder: %v", err)
	}

	go func() {
		for range fc.evictionTicker.C {
			fc.evictMemory()
			fc.evictDisk()
		}
	}()

	return nil
}

func (fc *FileCache) evictMemory() {
	fc.mutex.Lock()
	defer fc.mutex.Unlock()

	threshold := (fc.config.RAMBytesMax * 90) / 100
	for fc.usedMemoryBytes > threshold && fc.mruList.Len() > 0 {
		oldest := fc.mruList.Back()
		if oldest == nil {
			return
		}
		fileName := oldest.Value.(string)
		if entry, ok := fc.index.Load(fileName); ok && entry.InMemory {
			entry.Mutex.Lock()

			// check entry.InMemory again in case we are racing in this loop
			if entry.InMemory {
				entry.Data = nil
				entry.InMemory = false
				fc.usedMemoryBytes -= entry.Size
			}

			entry.Mutex.Unlock()

			evictionRAMCounter.Inc()
		}

		fc.mruList.Remove(oldest)

		delete(fc.mruMap, fileName)
	}
}

func (fc *FileCache) evictDisk() {
	fc.mutex.Lock()
	defer fc.mutex.Unlock()

	threshold := (fc.config.DiskBytesMax * 90) / 100
	for fc.usedDiskBytes > threshold && fc.mruList.Len() > 0 {
		// get the oldest file in the mruList
		oldest := fc.mruList.Back()
		if oldest == nil {
			return
		}

		// delete the file
		fileName := oldest.Value.(string)
		fullPath := filepath.Join(fc.config.DirPath, fileName)
		if _, err := os.Stat(fullPath); err == nil {
			if err := os.Remove(fullPath); err != nil {
				log.Printf("Error removing file: %v", err)
				continue
			}
			evictionDiskCounter.Inc()
		} else {
			log.Errorf("could not stat %s: %v", fullPath, err)
		}

		// get the index entry
		if entry, ok := fc.index.Load(fileName); ok {
			if entry.InMemory {
				fc.usedMemoryBytes -= entry.Size
			}
			fc.usedDiskBytes -= entry.Size
			fc.index.Delete(fileName)
		}

		// remove from mru
		fc.mruList.Remove(oldest)
		delete(fc.mruMap, fileName)
	}
}
