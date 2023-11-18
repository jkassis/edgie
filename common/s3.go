package common

import (
	"fmt"
	"io"
	"log"
	"os"

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
func S3FileUpload(
	s3Client *s3.S3,
	srcPath string,
	dstBucket string,
	dstPath string) error {

	file, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	_, err = s3Client.PutObject(
		&s3.PutObjectInput{
			Bucket: aws.String(dstBucket),
			Key:    aws.String(dstPath),
			Body:   file,
		})

	return err
}

func S3FileDownload(
	path string,
	bucketName string,
	s3Client *s3.S3) (rc io.ReadCloser, err error) {

	resp, err := s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, fmt.Errorf(S3ErrorPrefix+": failed to get object from S3:%v", err)
	}

	return resp.Body, nil
}
