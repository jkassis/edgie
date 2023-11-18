package service

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/jkassis/edgie/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CLI Options and Arg Parsing
const (
	OPT_CACHE_DIR            = "CACHE_DIR"
	OPT_CACHE_DISK_BYTES_MAX = "CACHE_DISK_BYTES_MAX"
	OPT_CACHE_EVICTION_TICK  = "CACHE_EVICTION_TICK"
	OPT_CACHE_RAM_BYTES_MAX  = "CACHE_RAM_BYTES_MAX"
	OPT_SYNC_DELAY           = "SYNC_DELAY"
	OPT_UPLOAD_DIR           = "UPLOAD_DIR"
)

// Prometheus Metrics
var (
	uploadCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "edgie_file_uploads_total",
		Help: "Total number of file uploads.",
	})
	uploadSizeHistogram = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "edgie_file_upload_size_bytes",
		Help:    "Histogram of file sizes for uploads.",
		Buckets: prometheus.LinearBuckets(1024, 1024*1024, 10), // Buckets from 1KB to 10MB
	})
	downloadCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "edgie_file_downloads_total",
		Help: "Total number of file downloads.",
	})
	downloadSizeHistogram = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "edgie_file_download_size_bytes",
		Help:    "Histogram of file sizes for downloads.",
		Buckets: prometheus.LinearBuckets(1024, 1024*1024, 10), // Similar to uploads
	})
)

func CmdInit(cmd *cobra.Command) {
	common.CmdInit(cmd)
	common.AWSCmdInit(cmd)
	common.S3CmdInit(cmd)

	cmd.PersistentFlags().String(OPT_CACHE_DIR, "/var/edgie/cache/download", "the directory to download files from")
	viper.BindPFlag(OPT_CACHE_DIR, cmd.PersistentFlags().Lookup(OPT_CACHE_DIR))

	cmd.PersistentFlags().String(OPT_UPLOAD_DIR, "/var/edgie/cache/upload", "the directory to upload files to")
	viper.BindPFlag(OPT_UPLOAD_DIR, cmd.PersistentFlags().Lookup(OPT_UPLOAD_DIR))

	cmd.PersistentFlags().Duration(OPT_SYNC_DELAY, 10*time.Second, "delay between s3 sync attempts")
	viper.BindPFlag(OPT_SYNC_DELAY, cmd.PersistentFlags().Lookup(OPT_SYNC_DELAY))

	cmd.PersistentFlags().Duration(OPT_CACHE_EVICTION_TICK, 10*time.Second, "delay between attempts at cache eviction")
	viper.BindPFlag(OPT_CACHE_EVICTION_TICK, cmd.PersistentFlags().Lookup(OPT_CACHE_EVICTION_TICK))

	cmd.PersistentFlags().Int64(OPT_CACHE_RAM_BYTES_MAX, int64(math.Pow(2, 9)), "max bytes for the cache ram")
	viper.BindPFlag(OPT_CACHE_RAM_BYTES_MAX, cmd.PersistentFlags().Lookup(OPT_CACHE_RAM_BYTES_MAX))

	cmd.PersistentFlags().Int64(OPT_CACHE_DISK_BYTES_MAX, int64(math.Pow(2, 9)), "max bytest for the cache disk")
	viper.BindPFlag(OPT_CACHE_DISK_BYTES_MAX, cmd.PersistentFlags().Lookup(OPT_CACHE_DISK_BYTES_MAX))
}

func CmdExecute(cmd *cobra.Command, args []string) (*Service, error) {
	common.CmdExecute(cmd, args)
	common.AWSCmdExecute(cmd, args)
	s3Conf := common.S3CmdExecute(cmd, args)

	cacheEvictionTick := viper.GetDuration(OPT_CACHE_EVICTION_TICK)
	if cacheEvictionTick == 0 {
		log.Fatal("CACHE_EVICTION_TICK not specified")
	}

	cacheDir := viper.GetString(OPT_CACHE_DIR)
	if cacheDir == "" {
		log.Fatal("CACHE_DIR not specified")
	}

	cacheDiskBytesMax := viper.GetInt64(OPT_CACHE_DISK_BYTES_MAX)
	if cacheDiskBytesMax == 0 {
		log.Fatal("CACHE_DISK_BYTES_MAX not specified")
	}

	cacheRAMBytesMax := viper.GetInt64(OPT_CACHE_RAM_BYTES_MAX)
	if cacheRAMBytesMax == 0 {
		log.Fatal("CACHE_RAM_BYTES_MAX not specified")
	}

	uploadDir := viper.GetString(OPT_UPLOAD_DIR)
	if uploadDir == "" {
		log.Fatal("CACHE_UPLOAD_DIR not specified")
	}

	syncDelay := viper.GetDuration(OPT_SYNC_DELAY)
	if syncDelay == 0 {
		log.Fatal("SYNC_DELAY not specified")
	}

	cache := common.NewFileCache(common.FileCacheConfig{
		EvictionTick: cacheEvictionTick,
		DirPath:      cacheDir,
		DiskBytesMax: cacheDiskBytesMax,
		RAMBytesMax:  cacheRAMBytesMax,
	})

	s := &Service{
		Cache: cache,
		Conf: Conf{
			UploadDir: uploadDir,
			S3:        s3Conf,
			SyncDelay: syncDelay,
		},
	}

	err := s.Start()
	if err != nil {
		return nil, fmt.Errorf("could not start the edgie service: %v", err)
	}
	return s, nil
}

type Conf struct {
	CacheDir  string
	UploadDir string
	S3        *common.S3Conf
	SyncDelay time.Duration
}

type Service struct {
	Conf  Conf
	Cache *common.FileCache
}

// startUploadSync synchronizes files from the upload directory to S3 and moves them to the serving directory.
func (s *Service) Start() error {
	if err := s.Cache.Start(); err != nil {
		return fmt.Errorf("failed to start cache: %v", err)
	}

	if err := os.MkdirAll(s.Conf.UploadDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	go s.S3SyncOnTickForever()

	return nil
}

func (s *Service) S3SyncOnTickForever() error {
	for {
		time.Sleep(s.Conf.SyncDelay)
		err := s.UploadsSyncToS3()
		if err != nil {
			log.Error(err)
		}
	}
}

func (s *Service) UploadsSyncToS3() error {
	files, err := filepath.Glob(filepath.Join(s.Conf.UploadDir, "**"))
	if err != nil {
		err = fmt.Errorf("could not read upload directory: %v", err)
		return err
	}

	var s3Client *s3.S3
	if len(files) > 0 {
		sess, _ := common.AWSSessionGet(s.Conf.S3.Region)
		s3Client = s3.New(sess, aws.NewConfig().WithRegion(s.Conf.S3.Region))
	}

	for _, file := range files {
		fileName := filepath.Base(file)
		fileKey := filepath.Join(s.Conf.CacheDir, fileName)

		// Upload file to S3
		err := common.S3FileUpload(s3Client, file, s.Conf.S3.Bucket, fileKey)
		if err != nil {
			return fmt.Errorf("s3 upload failed: %v", err)
		}

		// Remove from uploads in insert into cache
		newFilePath := filepath.Join(s.Conf.CacheDir, fileName)
		err = os.Rename(file, newFilePath)
		if err != nil {
			return fmt.Errorf("cache insert failed: %v", err)
		}
	}

	return nil
}

func (s *Service) Download(w http.ResponseWriter, r *http.Request) {
	downloadPath := filepath.Clean(s.Conf.CacheDir + r.URL.Path)
	fileInfo, err := os.Stat(downloadPath)
	if os.IsNotExist(err) {
		sess, _ := common.AWSSessionGet(s.Conf.S3.Region)
		s3Client := s3.New(sess, aws.NewConfig().WithRegion(s.Conf.S3.Region))
		err = common.S3FileDownload(w, r, downloadPath, s.Conf.S3.Bucket, s3Client)
		if err != nil {
			if strings.HasPrefix(err.Error(), common.S3ErrorPrefix) {
				http.NotFound(w, r)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
		fileInfo, err = os.Stat(downloadPath)
	}

	if err != nil {
		err = fmt.Errorf("could not stat %s", downloadPath)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		// Increment download counter and record file size
		downloadCounter.Inc()
		downloadSizeHistogram.Observe(float64(fileInfo.Size()))
		http.ServeFile(w, r, downloadPath)
	}
}

func (s *Service) Upload(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	uploadPath := filepath.Clean(s.Conf.UploadDir + "/" + r.URL.Path)
	uploadDir := path.Dir(uploadPath)

	// make the upload dir
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		err = fmt.Errorf("failed to create directory %s: %v", uploadDir, err)
		log.Error(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// make the upload path
	dst, err := os.Create(uploadPath)
	if err != nil {
		err = fmt.Errorf("failed to create the upload file %s: %v", uploadPath, err)
		log.Error(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// copy from Body to dst file
	fileSize, err := io.Copy(dst, r.Body)
	if err != nil {
		err = fmt.Errorf("filed to write to file %s: %v", uploadPath, err)
		log.Error(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	uploadCounter.Inc()
	uploadSizeHistogram.Observe(float64(fileSize))
	fmt.Fprintf(w, "File uploaded successfully: %s", r.URL.Path)
}
