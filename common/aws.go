package common

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var AWSSession *session.Session

const (
	OPT_AWS_REGION = "AWS_REGION"
)

func AWSCmdInit(Cmd *cobra.Command) {
	Cmd.PersistentFlags().String(OPT_AWS_REGION, "us-west-2", "AWS region")
	viper.BindPFlag(OPT_AWS_REGION, Cmd.PersistentFlags().Lookup(OPT_AWS_REGION))
}

func AWSCmdExecute(cmd *cobra.Command, args []string) {
	log.Warn("launch this with AWS_SDK_LOAD_CONFIG environment variable is set to a truthy value")
}

var awsSession *session.Session

func AWSSessionGet(region string) (*session.Session, error) {
	if awsSession == nil {
		var err error
		awsSession, err = session.NewSession(&aws.Config{
			Region:                        aws.String(region),
			MaxRetries:                    aws.Int(3),
			CredentialsChainVerboseErrors: aws.Bool(true),
			// HTTP client is required to fetch EC2 metadata values
			// having zero timeout on the default HTTP client sometimes makes
			// it fail with Credential error
			// https://github.com/aws/aws-sdk-go/issues/2914
			HTTPClient: &http.Client{Timeout: 10 * time.Second},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create AWS session: %v", err)
		}
	}
	return awsSession, nil
}
