package main

import (
	"path/filepath"
	"time"

	"fmt"
	"io"
	"net/http"
	"os"
	"path"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

// CLI Options and Arg Parsing
const (
	OPT_DOWNLOAD_CACHE_DIR = "DOWNLOAD_CACHE_DIR"
	OPT_LOG_LEVEL          = "LOG_LEVEL"
	OPT_PORT               = "PORT"
	OPT_S3_BUCKET          = "S3_BUCKET"
	OPT_S3_REGION          = "S3_REGION"
	OPT_UPLOAD_CACHE_DIR   = "UPLOAD_CACHE_DIR"
)

var rootCmd *cobra.Command

func cmdInit() {
	rootCmd = &cobra.Command{
		Use:   "edgie",
		Short: "edgie serves files from a local directory and falls back to S3 if they don't exist",
		Run:   execute,
	}
	rootCmd.PersistentFlags().String(OPT_DOWNLOAD_CACHE_DIR, "/var/edgie/pub", "Set the directory to serve files from")
	viper.BindPFlag(OPT_DOWNLOAD_CACHE_DIR, rootCmd.PersistentFlags().Lookup(OPT_DOWNLOAD_CACHE_DIR))

	rootCmd.PersistentFlags().String(OPT_PORT, "8080", "Port to serve API and metrics")
	viper.BindPFlag(OPT_PORT, rootCmd.PersistentFlags().Lookup(OPT_PORT))

	rootCmd.PersistentFlags().String(OPT_UPLOAD_CACHE_DIR, "/var/edgie/uploads", "Set the directory to upload files to")
	viper.BindPFlag(OPT_UPLOAD_CACHE_DIR, rootCmd.PersistentFlags().Lookup(OPT_UPLOAD_CACHE_DIR))

	rootCmd.PersistentFlags().String(OPT_S3_BUCKET, "edgie", "AWS S3 bucket name")
	viper.BindPFlag(OPT_S3_BUCKET, rootCmd.PersistentFlags().Lookup(OPT_S3_BUCKET))

	rootCmd.PersistentFlags().String(OPT_S3_REGION, "us-west-2", "AWS region for the S3 bucket")
	viper.BindPFlag(OPT_S3_REGION, rootCmd.PersistentFlags().Lookup(OPT_S3_REGION))

	rootCmd.PersistentFlags().String(OPT_LOG_LEVEL, "WARN", "Log level for the whole proc")
	viper.BindPFlag(OPT_LOG_LEVEL, rootCmd.PersistentFlags().Lookup(OPT_S3_REGION))

	viper.AutomaticEnv()
}

func logInit() {
	// Log as JSON instead of the default ASCII formatter.
	log.SetFormatter(&log.JSONFormatter{})

	// Output to stdout instead of the default stderr
	// Can be any io.Writer, see below for File example
	log.SetOutput(os.Stdout)

	// Only log the warning severity or above.
	logLevel := viper.GetString(OPT_LOG_LEVEL)
	level, err := log.ParseLevel(logLevel)
	if err != nil {
		log.Panic("LOG_LEVEL Invalid")
		panic(err)
	}

	log.SetLevel(level)
}

func main() {
	cmdInit()
	logInit()
	rootCmd.Execute()
}

func execute(cmd *cobra.Command, args []string) {
	log.Warn("launch this with AWS_SDK_LOAD_CONFIG environment variable is set to a truthy value")

	dataDir := viper.GetString(OPT_DOWNLOAD_CACHE_DIR)
	if dataDir == "" {
		log.Fatal("DATA_DIR not specified")
	}

	bucket := viper.GetString(OPT_S3_BUCKET)
	if bucket == "" {
		log.Fatal("S3_BUCKET not specified")
	}

	s3Region := viper.GetString(OPT_S3_REGION)
	if s3Region == "" {
		log.Fatal("S3_REGION not specified")
	}

	sess, _ := session.NewSession(&aws.Config{Region: aws.String(s3Region)})
	s3Client := s3.New(sess, aws.NewConfig().WithRegion(s3Region))

	download := func(w http.ResponseWriter, r *http.Request) {
		localFilePath := dataDir + r.URL.Path
		if _, err := os.Stat(localFilePath); os.IsNotExist(err) {
			s3err, err := fetchFromS3(w, r, localFilePath, bucket, s3Client)
			if s3err != nil {
				http.NotFound(w, r)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		// Increment download counter and record file size
		downloadCounter.Inc()
		fileInfo, err := os.Stat(localFilePath)
		if err == nil {
			downloadSizeHistogram.Observe(float64(fileInfo.Size()))
		}
		http.ServeFile(w, r, localFilePath)
	}

	upload := func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		uploadPath := viper.GetString("UPLOAD_DIR") + "/" + r.URL.Path
		dst, err := os.Create(path.Dir(uploadPath))
		if err != nil {
			http.Error(w, "Error creating the file", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		fileSize, err := io.Copy(dst, r.Body)
		if err != nil {
			http.Error(w, "Error writing the file", http.StatusInternalServerError)
			return
		}

		uploadCounter.Inc()
		uploadSizeHistogram.Observe(float64(fileSize))
		fmt.Fprintf(w, "File uploaded successfully: %s", r.URL.Path)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			download(w, r)
		} else if r.Method == "POST" {
			upload(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
	})

	go syncUploadsToS3(s3Client, viper.GetString(OPT_UPLOAD_CACHE_DIR), viper.GetString(OPT_DOWNLOAD_CACHE_DIR), bucket)

	port := viper.GetString(OPT_PORT)
	http.Handle("/metrics", promhttp.Handler())
	log.Printf("Serving metrics on HTTP port: %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func fetchFromS3(w http.ResponseWriter,
	r *http.Request,
	localFilePath string,
	bucketName string,
	s3Client *s3.S3) (s3error error, err error) {
	resp, err := s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(r.URL.Path),
	})
	if err != nil {
		return fmt.Errorf("failed to get object from S3:%v", err), nil
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(path.Dir(localFilePath), os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

	localFile, err := os.Create(localFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %v", err)
	}
	defer localFile.Close()

	if _, err := io.Copy(localFile, resp.Body); err != nil {
		return nil, fmt.Errorf("failed to write to file: %v", err)
	}

	return nil, nil
}

// syncUploadsToS3 synchronizes files from the upload directory to S3 and moves them to the serving directory.
func syncUploadsToS3(s3Client *s3.S3, uploadDir string, dataDir string, bucket string) {
	for {
		files, err := filepath.Glob(filepath.Join(uploadDir, "*"))
		if err != nil {
			log.Errorf("Error reading upload directory: %v", err)
			continue
		}

		for _, file := range files {
			fileName := filepath.Base(file)
			fileKey := filepath.Join(dataDir, fileName)

			// Upload file to S3
			err := uploadFileToS3(s3Client, file, bucket, fileKey)
			if err != nil {
				log.Errorf("Error uploading file to S3: %v", err)
				continue
			}

			// Move file to serving directory
			newFilePath := filepath.Join(dataDir, fileName)
			err = os.Rename(file, newFilePath)
			if err != nil {
				log.Errorf("Error moving file to serving directory: %v", err)
				continue
			}
		}

		time.Sleep(1 * time.Hour) // Run every hour
	}
}

// uploadFileToS3 uploads a file to an S3 bucket.
func uploadFileToS3(s3Client *s3.S3, filePath string, bucket string, key string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	_, err = s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   file,
	})

	return err
}

// Execute executes the root command.
func Execute() error {
	return rootCmd.Execute()
}
