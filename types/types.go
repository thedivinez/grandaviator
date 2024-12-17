package types

import (
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
	"github.com/thedivinez/go-libs/mailer"
	"github.com/thedivinez/go-libs/storage"
	"github.com/thedivinez/go-libs/utils"
)

type AuthServiceConfig struct {
	mailer.MailerConfigs
	storage.MongoDBConfig
	Port        string `json:"PORT"`
	Password    string `json:"PASSWORD"`
	Username    string `json:"USERNAME"`
	ApiKey      string `json:"API_KEY"`
	Host        string `json:"HOST_NAME"`
	ApiHash     string `json:"API_HASH"`
	ApiSecret   string `json:"API_SECRET"`
	ClientURI   string `json:"CLIENT_URI"`
	FileServer  string `json:"FILE_SERVER"`
	ServiceName string `json:"SERVICE_NAME"`
	MerchantId  string `json:"MERCHANT_ID"`
	GatewayHost string `json:"GATEWAY_HOST"`
	Redis       string `json:"REDIS_ADDRESS"`
	AuthServer  string `json:"AUTH_SERVER"`
}

func (c *AuthServiceConfig) ReadFromEnv() error {
	if configs, err := godotenv.Read(); err != nil {
		return errors.WithStack(err)
	} else {
		return utils.Transcode(configs, c)
	}
}
