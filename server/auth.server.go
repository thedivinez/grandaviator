package server

import (
	"fmt"
	"log"
	"net"

	"github.com/thedivinez/go-libs/messaging"
	"github.com/thedivinez/go-libs/services/auth"
	"github.com/thedivinez/go-libs/services/aviator"
	"github.com/thedivinez/go-libs/storage"
	"github.com/thedivinez/go-libs/utils"
	"github.com/thedivinez/grandaviator/types"
	"go.mongodb.org/mongo-driver/bson"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
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
	if conn, err := utils.ConnectService(server.config.AuthServer); err != nil {
		return nil, err
	} else {
		server.auth = auth.NewAuthenticationClient(conn)
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
	if listener, err := net.Listen("tcp", fmt.Sprintf(":%s", server.config.Port)); err != nil {
		return err
	} else {
		log.Printf("starting service on port:%s", server.config.Port)
		srv := grpc.NewServer(grpc.UnaryInterceptor(utils.OutgoingInterceptor))
		aviator.RegisterAviatorServer(srv, server)
		reflection.Register(srv)
		return srv.Serve(listener)
	}
}
