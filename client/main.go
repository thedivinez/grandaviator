package main

import (
	"context"
	"log"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/thedivinez/go-libs/services/auth"
	"github.com/thedivinez/go-libs/utils"
)

type Client struct {
	redis         *redis.Client
	auth          auth.AuthenticationClient
	Redis         string `json:"REDIS_ADDRESS"`
	AuthSeverAddr string `json:"AUTH_SERVER_ADDRESS"`
}

func NewAuthClient() (*Client, error) {
	client := &Client{}
	if configs, err := godotenv.Read(); err != nil {
		return nil, err
	} else if err := utils.Transcode(configs, &client); err != nil {
		return nil, err
	}
	client.redis = redis.NewClient(&redis.Options{Addr: client.Redis})
	if conn, err := utils.ConnectService(client.AuthSeverAddr); err != nil {
		return nil, err
	} else {
		client.auth = auth.NewAuthenticationClient(conn)
	}
	return client, nil
}

func main() {
	client, err := NewAuthClient()
	if err != nil {
		log.Fatal(err)
	}
	if res, err := client.auth.UpdateOnlineStatus(context.Background(), &auth.UpdateOnlineStatusRequest{}); err != nil {
		if err, ok := err.(*utils.ServiceError); ok {
			log.Println(err.Code)
		}
		log.Fatal(err)
	} else {
		log.Println(res)
	}
}
