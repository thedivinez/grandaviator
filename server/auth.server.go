package server

import (
	"github.com/thedivinez/go-libs/messaging"
	"github.com/thedivinez/go-libs/services"
	"github.com/thedivinez/go-libs/services/auth"
	"github.com/thedivinez/go-libs/services/aviator"
	"github.com/thedivinez/go-libs/storage"
	"github.com/thedivinez/go-libs/utils"
	"github.com/thedivinez/grandaviator/types"
	"go.mongodb.org/mongo-driver/bson"
)

type Server struct {
	db        storage.Database
	log       *utils.ServerLogger
	redis     *storage.RedisCache
	messaging *messaging.Messenger
	config    *types.AuthServiceConfig
	auth      auth.AuthenticationClient
}

func NewServer() (*Server, error) {
	server := &Server{log: utils.NewLogger(), config: &types.AuthServiceConfig{}}
	if err := server.config.ReadFromEnv(); err != nil {
		return nil, err
	}
	if msg, err := messaging.NewClient(server.config.Redis, 1); err == nil {
		server.messaging = msg
	} else {
		return nil, err
	}
	if conn, err := auth.Connect(server.config.AuthServer); err != nil {
		return nil, err
	} else {
		server.auth = conn
	}
	server.redis = storage.NewRedisCache(server.config.Redis, 1)
	server.db = storage.NewMongoStorage(server.config.MongoDBConfig)
	clients := []*aviator.PlaneSettings{}
	if err := server.db.Find(CLIENTS_COLLECTION, bson.M{}, &clients); err != nil {
		return nil, err
	}
	for idx := range clients {
		server.initializePlane(clients[idx].OrgID)
	}
	return server, nil
}

func (server *Server) Start() error {
	if service, err := services.NewService(server.config.Port); err != nil {
		return err
	} else {
		aviator.RegisterAviatorServer(service.Server, server)
		return service.Start()
	}
}
