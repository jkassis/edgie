package common

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	OPT_S3_BUCKET = "S3_BUCKET"
	S3ErrorPrefix = "s3error"
)

type S3Conf struct {
	Bucket string
	Region string
}

func S3CmdInit(Cmd *cobra.Command) {
	Cmd.PersistentFlags().String(OPT_S3_BUCKET, "edgie", "AWS S3 bucket name")
	viper.BindPFlag(OPT_S3_BUCKET, Cmd.PersistentFlags().Lookup(OPT_S3_BUCKET))
}

func S3CmdExecute(cmd *cobra.Command, args []string) *S3Conf {
	s3Bucket := viper.GetString(OPT_S3_BUCKET)
	if s3Bucket == "" {
		log.Fatal("S3_BUCKET not specified")
	}

	s3Region := viper.GetString(OPT_AWS_REGION)
	if s3Region == "" {
		log.Fatal("AWS_REGION not specified")
	}

	return &S3Conf{
		s3Bucket,
		s3Region,
	}
}

// S3FileUpload uploads a file to an S3 bucket.
func S3FileUpload(s3Client *s3.S3,
	filePath string,
	bucket string,
	key string) error {
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

func S3FileDownload(w http.ResponseWriter,
	r *http.Request,
	localFilePath string,
	bucketName string,
	s3Client *s3.S3) (err error) {

	resp, err := s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(r.URL.Path),
	})
	if err != nil {
		return fmt.Errorf(S3ErrorPrefix+": failed to get object from S3:%v", err)
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(path.Dir(localFilePath), os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	localFile, err := os.Create(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer localFile.Close()

	if _, err := io.Copy(localFile, resp.Body); err != nil {
		return fmt.Errorf("failed to write to file: %v", err)
	}

	return nil
}
